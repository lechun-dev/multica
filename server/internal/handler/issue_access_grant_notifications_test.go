package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestIssueAccessGrantNotificationsFollowSavedDirectChanges(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	enableAccessRequestRobotForTest(t)
	for _, scope := range []string{"projectless", "project"} {
		t.Run(scope, func(t *testing.T) {
			ws := dbfx.Workspace(t, "Direct grant notices", "direct-grant-"+scope)
			fx := testutil.New(testPool, ws, testUserID)
			fx.Member(t, ws, testUserID, "owner")
			person := dbfx.User(t, "Shared task recipient", "shared-task-"+scope+"@example.test")
			fx.Member(t, ws, person, "member")
			for _, user := range []string{testUserID, person} {
				seedAccessRequestDingTalkBinding(t, fx, user)
			}
			cleanupAccessRequestDingTalkQueues(t, ws)
			cols := testutil.Cols{}
			if scope == "project" {
				project := fx.Project(t, "Sharing project")
				cols["project_id"] = project
				// An inherited permission must not suppress an explicit share.
				fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": ws, "project_id": project, "subject_type": "user", "subject_id": person, "role_key": "member", "source": "manual"})
			}
			issue := fx.Issue(t, "Directly shared task", cols)
			h := *testHandler
			h.cfg.AppURL = "https://app.example.test"
			request := issueAccessControlRequest{ExpectedVersion: 1, ProjectAccessMode: projectauth.ProjectAccessInherit,
				Grants: []issueAccessControlGrant{{SubjectType: projectauth.SubjectUser, SubjectID: person, Role: "member", Scope: projectauth.RoleScopeTask}}}
			save := func(status int) issueAccessControlResponse {
				t.Helper()
				var result issueAccessControlResponse
				testutil.Call(t, h.PatchIssueAccessControl, issueAccessHTTPRequest(testUserID, ws, http.MethodPatch, issue, "", request)).Want(status).JSON(&result)
				return result
			}
			state := save(http.StatusOK)
			assertIssueAccessGrantQueues(t, ws, person, 1)
			save(http.StatusOK) // Identical transport retry, including a stale version.
			assertIssueAccessGrantQueues(t, ws, person, 1)

			request.ExpectedVersion = state.PolicyVersion
			request.Grants[0].Role = "viewer"
			state = save(http.StatusOK)
			assertIssueAccessGrantQueues(t, ws, person, 2)
			request.ExpectedVersion = state.PolicyVersion
			expiry := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
			request.Grants[0].ExpiresAt = &expiry
			state = save(http.StatusOK)
			assertIssueAccessGrantQueues(t, ws, person, 3)

			var robot, personal string
			fx.QueryRow(t, `SELECT message_text FROM dingtalk_notify_outbox WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 1`, ws).Scan(&robot)
			fx.QueryRow(t, `SELECT markdown FROM dingtalk_personal_message WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 1`, ws).Scan(&personal)
			for _, message := range []string{robot, personal} {
				for _, part := range []string{"任务授权", "调整", "可查看", "有效期", "Directly shared task", "https://app.example.test/direct-grant-" + scope + "/issues/", "[打开任务]"} {
					if !strings.Contains(message, part) {
						t.Errorf("message missing %q: %s", part, message)
					}
				}
			}

			request.ExpectedVersion = state.PolicyVersion
			request.Grants[0].ExpiresAt = nil
			state = save(http.StatusOK)
			assertIssueAccessGrantQueues(t, ws, person, 4)
			request.ExpectedVersion = state.PolicyVersion
			request.Grants = nil
			state = save(http.StatusOK) // Revocation does not add a notification.
			assertIssueAccessGrantQueues(t, ws, person, 4)
			request.ExpectedVersion = state.PolicyVersion
			request.Grants = []issueAccessControlGrant{{SubjectType: projectauth.SubjectUser, SubjectID: person, Role: "member"}}
			save(http.StatusOK) // A later legitimate regrant must notify again.
			assertIssueAccessGrantQueues(t, ws, person, 5)
		})
	}
}

func assertIssueAccessGrantQueues(t *testing.T, workspace, recipient string, want int) {
	t.Helper()
	for _, query := range []string{
		`SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1 AND target_id=$2 AND channel_type='p2p'`,
		`SELECT count(*) FROM dingtalk_personal_message WHERE workspace_id=$1 AND recipient_user_id=$2`,
	} {
		var got int
		if err := testPool.QueryRow(context.Background(), query, workspace, recipient).Scan(&got); err != nil || got != want {
			t.Fatalf("notification rows=%d, want %d; err=%v; query=%s", got, want, err, query)
		}
	}
}

func TestIssueAccessGrantNotificationsRollback(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	enableAccessRequestRobotForTest(t)
	for _, failure := range []string{"commit", "robot queue", "personal queue", "preview"} {
		t.Run(failure, func(t *testing.T) {
			ws := dbfx.Workspace(t, "Grant rollback", "grant-rollback")
			fx := testutil.New(testPool, ws, testUserID)
			fx.Member(t, ws, testUserID, "owner")
			person := dbfx.User(t, "Rollback recipient", "grant-rollback@example.test")
			fx.Member(t, ws, person, "member")
			for _, user := range []string{testUserID, person} {
				seedAccessRequestDingTalkBinding(t, fx, user)
			}
			cleanupAccessRequestDingTalkQueues(t, ws)
			issue := fx.Issue(t, "Rollback share")
			h := *testHandler
			request := issueAccessControlRequest{ExpectedVersion: 1, ProjectAccessMode: projectauth.ProjectAccessRestricted,
				Grants: []issueAccessControlGrant{{SubjectType: projectauth.SubjectUser, SubjectID: person, Role: "member"}}}
			switch failure {
			case "commit":
				h.TxStarter = rollbackOnCommitTxStarter{pool: testPool}
			case "robot queue":
				h.TxStarter = accessRequestFailingDingTalkStarter{pool: testPool, robot: true}
			case "personal queue":
				h.TxStarter = accessRequestFailingDingTalkStarter{pool: testPool, personal: true}
			}
			if failure == "preview" {
				testutil.Call(t, h.PreviewIssueAccessControl, issueAccessHTTPRequest(testUserID, ws, http.MethodPost, issue, "", request)).Want(http.StatusOK)
			} else {
				testutil.Call(t, h.PatchIssueAccessControl, issueAccessHTTPRequest(testUserID, ws, http.MethodPatch, issue, "", request)).Want(http.StatusInternalServerError)
			}
			assertIssueAccessGrantQueues(t, ws, person, 0)
			for _, query := range []string{
				`SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND issue_id=$2`,
				`SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND source='manual'`,
				`SELECT count(*) FROM projectauth_issue_policies WHERE workspace_id=$1 AND issue_id=$2 AND policy_version>1`,
			} {
				var count int
				fx.QueryRow(t, query, ws, issue).Scan(&count)
				if count != 0 {
					t.Errorf("rollback left %d rows: %s", count, query)
				}
			}
		})
	}
}

func TestIssueAccessGrantNotificationsRespectScopeAndBindings(t *testing.T) {
	for _, tc := range []struct {
		name            string
		robot, personal int
	}{
		{"both", 1, 1}, {"self", 0, 0}, {"everyone", 0, 0}, {"organization", 0, 0},
		{"login only", 0, 0}, {"inactive recipient", 0, 0}, {"disabled recipient", 0, 0},
		{"unbound recipient", 0, 0}, {"unbound sender", 1, 0}, {"robot unconfigured", 0, 1},
		{"outside workspace", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableProjectAuthorizationForTest(t)
			enableAccessRequestRobotForTest(t)
			if tc.name == "robot unconfigured" {
				t.Setenv("DINGTALK_ROBOT_CODE", "")
			}
			ws := dbfx.Workspace(t, "Grant bindings", "grant-bindings")
			fx := testutil.New(testPool, ws, testUserID)
			fx.Member(t, ws, testUserID, "owner")
			person := dbfx.User(t, "Binding recipient", "grant-binding@example.test")
			if tc.name != "outside workspace" {
				fx.Member(t, ws, person, "member")
			}
			if tc.name != "unbound sender" {
				seedAccessRequestDingTalkBinding(t, fx, testUserID)
			}
			if tc.name != "unbound recipient" {
				seedAccessRequestDingTalkBinding(t, fx, person)
			}
			if tc.name == "login only" {
				fx.Exec(t, `UPDATE dingtalk_notify_identities SET login_only=true WHERE multica_user_id=$1`, person)
			}
			if tc.name == "inactive recipient" {
				fx.Exec(t, `UPDATE dingtalk_notify_identities SET active=false WHERE multica_user_id=$1`, person)
			}
			if tc.name == "disabled recipient" {
				fx.Exec(t, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, person)
			}
			cleanupAccessRequestDingTalkQueues(t, ws)
			issue := fx.Issue(t, "Scope and binding checks")
			grant := issueAccessControlGrant{SubjectType: projectauth.SubjectUser, SubjectID: person, Role: "member"}
			switch tc.name {
			case "self":
				grant.SubjectID = testUserID
			case "everyone":
				grant.SubjectType, grant.SubjectID = projectauth.SubjectEveryone, ""
			case "organization":
				organization := fx.Insert(t, "projectauth_organizations", testutil.Cols{"workspace_id": ws, "provider": "test", "external_id": "grant-notification-org", "name": "Notification department"})
				fx.InsertNoID(t, "projectauth_organization_members", testutil.Cols{"workspace_id": ws, "organization_id": organization, "user_id": person}, "workspace_id=$1 AND organization_id=$2 AND user_id=$3", ws, organization, person)
				grant.SubjectType, grant.SubjectID = projectauth.SubjectOrganization, organization
			}
			request := issueAccessControlRequest{ExpectedVersion: 1, ProjectAccessMode: projectauth.ProjectAccessRestricted, Grants: []issueAccessControlGrant{grant}}
			status := http.StatusOK
			if tc.name == "outside workspace" {
				status = http.StatusBadRequest
			}
			testutil.Call(t, testHandler.PatchIssueAccessControl, issueAccessHTTPRequest(testUserID, ws, http.MethodPatch, issue, "", request)).Want(status)
			for _, queue := range []struct {
				query string
				want  int
			}{
				{`SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1`, tc.robot},
				{`SELECT count(*) FROM dingtalk_personal_message WHERE workspace_id=$1`, tc.personal},
			} {
				var count int
				fx.QueryRow(t, queue.query, ws).Scan(&count)
				if count != queue.want {
					t.Errorf("queue rows=%d, want %d: %s", count, queue.want, queue.query)
				}
			}
		})
	}
}
