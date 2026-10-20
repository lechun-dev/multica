package handler

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func ensureDepartureBindings(t *testing.T) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	cfg := *testPool.Config().ConnConfig
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	schemaDB := stdlib.OpenDB(cfg)
	defer schemaDB.Close()
	if err := notify.EnsureSchema(context.Background(), schemaDB); err != nil {
		t.Fatal(err)
	}
}

func bindDepartureUser(t *testing.T, userID, dingID string) {
	t.Helper()
	dbfx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": userID, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
}

func TestDingTalkDeparturesPreserveBusinessRecordsAndRevokeSessions(t *testing.T) {
	ensureDepartureBindings(t)
	ctx := context.Background()
	uid := dbfx.User(t, "Departed user", "departure-business@example.test")
	memberID := dbfx.Member(t, testWorkspaceID, uid, "member")
	dingID := "departure-" + uid
	bindDepartureUser(t, uid, dingID)
	projectID := dbfx.Project(t, "Preserved departure project")
	issueID := dbfx.Issue(t, "Preserved departure task", testutil.Cols{"project_id": projectID, "assignee_type": "member", "assignee_id": uid})
	commentID := dbfx.Comment(t, issueID, "Preserved comment", testutil.Cols{"author_id": uid})
	grantID := dbfx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": projectID, "issue_id": issueID, "subject_type": "user", "subject_id": uid, "role_key": "member"})
	before, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]string{}
	for table, id := range map[string]string{"project": projectID, "comment": commentID, "projectauth_access_grants": grantID, "member": memberID} {
		var data string
		dbfx.QueryRow(t, `SELECT row_to_json(r)::text FROM `+table+` r WHERE id=$1`, id).Scan(&data)
		snapshots[table] = data
	}
	user, err := testHandler.Queries.GetUser(ctx, parseUUID(uid))
	if err != nil {
		t.Fatal(err)
	}
	oldJWT, err := testHandler.issueJWT(user)
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.GeneratePATToken()
	if err != nil {
		t.Fatal(err)
	}
	patID := dbfx.Insert(t, "personal_access_token", testutil.Cols{"user_id": uid, "name": "departure-test", "token_hash": auth.HashToken(token), "token_prefix": token[:12]})
	apply := func(commit bool) DingTalkSuspensionResult {
		t.Helper()
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		result, err := testHandler.SuspendDingTalkDepartures(ctx, tx, testWorkspaceID, testUserID, []string{dingID, dingID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	if got := apply(false); len(got.Changes) != 1 {
		t.Fatalf("rollback preparation: %+v", got)
	}
	state, err := testHandler.Queries.GetUserAccountAccess(ctx, parseUUID(uid))
	if err != nil || state.AccountDisabledAt.Valid || state.SessionVersion != 0 {
		t.Fatalf("rollback changed account: %+v %v", state, err)
	}
	var revoked bool
	dbfx.QueryRow(t, `SELECT revoked FROM personal_access_token WHERE id=$1`, patID).Scan(&revoked)
	if revoked {
		t.Fatal("rollback revoked PAT")
	}
	if got := apply(true); len(got.Changes) != 1 || len(got.Changes[0].RevokedHashes) != 1 {
		t.Fatalf("suspension=%+v", got)
	}
	if err := auth.CheckAccountToken(ctx, testHandler.Queries, uid, oldJWT); err != auth.ErrTemporarilyDisabledUser {
		t.Fatalf("old session=%v", err)
	}
	if got := apply(true); len(got.Changes) != 0 || got.AlreadyDisabled != 1 {
		t.Fatalf("retry=%+v", got)
	}
	state, err = testHandler.Queries.GetUserAccountAccess(ctx, parseUUID(uid))
	if err != nil || state.SessionVersion != 1 {
		t.Fatalf("retry changed version: %+v %v", state, err)
	}
	after, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("task changed: %v", err)
	}
	for table, id := range map[string]string{"project": projectID, "comment": commentID, "projectauth_access_grants": grantID, "member": memberID} {
		var data string
		dbfx.QueryRow(t, `SELECT row_to_json(r)::text FROM `+table+` r WHERE id=$1`, id).Scan(&data)
		if data != snapshots[table] {
			t.Fatalf("%s changed", table)
		}
	}
}

func TestDingTalkDeparturesRespectAccountAuthorityAndCurrentEmployment(t *testing.T) {
	ensureDepartureBindings(t)
	cases := []string{"owner", "self", "cross_workspace", "not_member", "unmatched", "current_local_user", "manual_disabled", "admin_actor", "disabled_actor"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			uid := dbfx.User(t, "Protected departure", name+"-departure@example.test")
			role := "member"
			if name == "owner" {
				role = "owner"
			}
			if name == "self" {
				uid = testUserID
			}
			if name != "self" && name != "not_member" {
				dbfx.Member(t, testWorkspaceID, uid, role)
			}
			dingID := "departure-protected-" + name + "-" + uid
			if name != "unmatched" {
				bindDepartureUser(t, uid, dingID)
			}
			if name == "cross_workspace" {
				workspace := dbfx.Workspace(t, "Other departure scope", "departure-"+uid)
				dbfx.Member(t, workspace, uid, "member")
			}
			if name == "manual_disabled" {
				dbfx.Exec(t, `UPDATE "user" SET account_disabled_at=now(),session_version=7 WHERE id=$1`, uid)
			}
			actorID := testUserID
			if name == "admin_actor" {
				actorID = dbfx.User(t, "Sync admin", "departure-admin@example.test")
				dbfx.Member(t, testWorkspaceID, actorID, "admin")
			}

			if name == "disabled_actor" {
				actorID = dbfx.User(t, "Disabled sync owner", "departure-disabled-owner@example.test")
				dbfx.Member(t, testWorkspaceID, actorID, "owner")
				dbfx.Exec(t, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, actorID)
			}
			var current []string
			// 2026-10-20 coder(lq): A rehire may retain the local user under a new DingTalk ID.
			if name == "current_local_user" || name == "manual_disabled" {
				current = []string{uid}
			}
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			result, err := testHandler.SuspendDingTalkDepartures(ctx, tx, testWorkspaceID, actorID, []string{dingID}, current)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Changes) != 0 || result.Skipped != 1 {
				t.Fatalf("protected suspension=%+v", result)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			state, err := testHandler.Queries.GetUserAccountAccess(ctx, parseUUID(uid))
			if err != nil {
				t.Fatal(err)
			}
			if name == "manual_disabled" {
				if !state.AccountDisabledAt.Valid || state.SessionVersion != 7 {
					t.Fatalf("manual suspension changed: %+v", state)
				}
			} else if state.AccountDisabledAt.Valid || state.SessionVersion != 0 {
				t.Fatalf("protected state changed: %+v", state)
			}
		})
	}
}

func TestDingTalkDepartureSyncRequiresHumanOwner(t *testing.T) {
	ensureDepartureBindings(t)
	for _, source := range []string{"", "task_token", "cloud_pat"} {
		req := newRequest(http.MethodPost, "/sync", nil)
		req.Header.Set("X-Actor-Source", source)
		allowed, err := testHandler.CanSyncAccountAccess(req, testWorkspaceID)
		if err != nil || allowed != (source == "") {
			t.Fatalf("source %q: allowed=%v err=%v", source, allowed, err)
		}
	}
}

// 2026-10-20 coder(lq): A background sync bypasses HTTP invalidation; the
// committed organization event must refresh all authorization projections,
// without discarding unrelated workspaces' cached reads.
func TestDingTalkDirectorySyncEventInvalidatesWorkspaceCaches(t *testing.T) {
	ctx := context.Background()
	org := NewOrganizationReadCache(nil, t.Name())
	list := NewFrequentReadCache(nil, t.Name())
	permission := NewPermissionReadCache(nil, t.Name())
	bus := events.New()
	bus.SubscribeAll(org.Observe)
	bus.SubscribeAll(list.Observe)
	bus.SubscribeAll(permission.Observe)
	readers := map[string]func(string) (string, error){
		"organization": func(ws string) (string, error) { return org.generation(ctx, ws) },
		"list":         func(ws string) (string, error) { return list.generation(ctx, ws) },
		"permission":   func(ws string) (string, error) { return permission.cache.generation(ctx, ws) },
	}
	before := map[string]string{}
	for kind, read := range readers {
		for _, ws := range []string{"affected", "unrelated"} {
			version, err := read(ws)
			if err != nil {
				t.Fatal(err)
			}
			before[kind+ws] = version
		}
	}
	bus.Publish(events.Event{Type: "organization:synced", WorkspaceID: "affected", ActorType: "system"})
	for kind, read := range readers {
		for _, ws := range []string{"affected", "unrelated"} {
			version, err := read(ws)
			if err != nil {
				t.Fatal(err)
			}
			if (version != before[kind+ws]) != (ws == "affected") {
				t.Errorf("%s invalidation escaped its workspace: %s", kind, ws)
			}
		}
	}
}
