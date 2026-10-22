package handler

import (
	"context"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func accountAccessRequest(t *testing.T, actor, workspace, member string, disabled bool) *testutil.Response {
	t.Helper()
	req := newRequestAs(actor, http.MethodPatch, "/api/workspaces/"+workspace+"/members/"+member+"/account-access", map[string]any{"disabled": disabled})
	req.Header.Set("X-Workspace-ID", workspace)
	req = withURLParams(req, "id", workspace, "memberId", member)
	return testutil.Call(t, testHandler.SetMemberAccountAccess, req)
}

func TestMemberAccountSuspensionPreservesBusinessRecords(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	userID := dbfx.User(t, "Suspended account", "suspension@example.test")
	memberID := dbfx.Member(t, testWorkspaceID, userID, "member")
	projectID := dbfx.Project(t, "Keep project")
	issueID := dbfx.Issue(t, "Keep assignment", testutil.Cols{"project_id": projectID, "assignee_type": "member", "assignee_id": userID})
	commentID := dbfx.Comment(t, issueID, "Keep original author and content", testutil.Cols{"author_id": userID})
	grantID := dbfx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": projectID, "issue_id": issueID, "subject_type": "user", "subject_id": userID, "role_key": "member"})
	projectBefore, commentBefore, grantBefore := "", "", ""
	dbfx.QueryRow(t, `SELECT row_to_json(p)::text FROM project p WHERE id=$1`, projectID).Scan(&projectBefore)
	dbfx.QueryRow(t, `SELECT row_to_json(c)::text FROM comment c WHERE id=$1`, commentID).Scan(&commentBefore)
	dbfx.QueryRow(t, `SELECT row_to_json(g)::text FROM projectauth_access_grants g WHERE id=$1`, grantID).Scan(&grantBefore)
	issueBefore, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	oldUser, err := testHandler.Queries.GetUser(ctx, parseUUID(userID))
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := testHandler.issueJWT(oldUser)
	if err != nil {
		t.Fatal(err)
	}
	authStatus := func(token string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		middleware.Auth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rec, req)
		return rec.Code
	}
	if got := authStatus(oldToken); got != 200 {
		t.Fatalf("before suspension=%d", got)
	}
	accountAccessRequest(t, testUserID, testWorkspaceID, memberID, true).Want(http.StatusOK)
	accountAccessRequest(t, testUserID, testWorkspaceID, memberID, true).Want(http.StatusOK)
	var listed []MemberWithUserResponse
	listReq := withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/members", nil), "id", testWorkspaceID)
	testutil.Call(t, testHandler.ListMembersWithUser, listReq).Want(http.StatusOK).JSON(&listed)
	found := false
	for _, member := range listed {
		if member.ID == memberID {
			found = true
			if !member.AccountDisabled || !member.CanManageAccount {
				t.Fatalf("member list lost suspension state or owner action: %+v", member)
			}
		}
	}
	if !found {
		t.Fatal("suspended account disappeared from member list")
	}
	state, err := testHandler.Queries.GetUserAccountAccess(ctx, parseUUID(userID))
	if err != nil {
		t.Fatal(err)
	}
	if !state.AccountDisabledAt.Valid || state.SessionVersion != 1 {
		t.Fatalf("state=%+v", state)
	}
	if got := authStatus(oldToken); got != 401 {
		t.Fatalf("old session still works=%d", got)
	}
	disabledUser, _ := testHandler.Queries.GetUser(ctx, parseUUID(userID))
	if _, err := testHandler.issueJWT(disabledUser); err != auth.ErrTemporarilyDisabledUser {
		t.Fatalf("disabled login=%v", err)
	}
	// Read with the other member, not a newly invented permission bypass.
	req := newRequest(http.MethodGet, "/api/issues/"+issueID, nil)
	testutil.Call(t, testHandler.GetIssue, withURLParam(req, "id", issueID)).Want(http.StatusOK)
	accountAccessRequest(t, testUserID, testWorkspaceID, memberID, false).Want(http.StatusOK)
	if got := authStatus(oldToken); got != 401 {
		t.Fatalf("re-enable resurrected session=%d", got)
	}
	freshUser, _ := testHandler.Queries.GetUser(ctx, parseUUID(userID))
	freshToken, err := testHandler.issueJWT(freshUser)
	if err != nil {
		t.Fatal(err)
	}
	if got := authStatus(freshToken); got != 200 {
		t.Fatalf("fresh login=%d", got)
	}
	// An already-authenticated socket keeps the version of its original token.
	// HTTP renewal does not replace that token in the socket; expiry alone must
	// not force logout, while a new HTTP request must still reject expired JWTs.
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": userID, "session_version": freshUser.SessionVersion, "exp": time.Now().Add(-time.Minute).Unix()}).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckAccountToken(ctx, testHandler.Queries, userID, expired); err != nil {
		t.Fatalf("active socket lost access after HTTP renewal: %v", err)
	}
	if got := authStatus(expired); got != 401 {
		t.Fatalf("expired HTTP token accepted: %d", got)
	}

	after, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(issueBefore, after) {
		t.Fatal("suspension changed task data")
	}
	if _, err := testHandler.Queries.GetMember(ctx, parseUUID(memberID)); err != nil {
		t.Fatalf("membership removed: %v", err)
	}
	projectAfter, commentAfter, grantAfter := "", "", ""
	dbfx.QueryRow(t, `SELECT row_to_json(p)::text FROM project p WHERE id=$1`, projectID).Scan(&projectAfter)
	dbfx.QueryRow(t, `SELECT row_to_json(c)::text FROM comment c WHERE id=$1`, commentID).Scan(&commentAfter)
	dbfx.QueryRow(t, `SELECT row_to_json(g)::text FROM projectauth_access_grants g WHERE id=$1`, grantID).Scan(&grantAfter)
	if projectBefore != projectAfter || commentBefore != commentAfter || grantBefore != grantAfter {
		t.Fatal("suspension mutated project, comment or task grant")
	}
	if dbfx.Count(t, `SELECT count(*) FROM project WHERE id=$1`, projectID) != 1 {
		t.Fatal("project removed")
	}
}

func TestMemberAccountSuspensionAuthority(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	userID := dbfx.User(t, "Account target", "suspension-authority@example.test")
	memberID := dbfx.Member(t, testWorkspaceID, userID, "member")
	adminID := dbfx.User(t, "Account admin", "suspension-admin@example.test")
	dbfx.Member(t, testWorkspaceID, adminID, "admin")
	accountAccessRequest(t, userID, testWorkspaceID, memberID, true).Want(http.StatusForbidden)
	accountAccessRequest(t, adminID, testWorkspaceID, memberID, true).Want(http.StatusForbidden)
	owner := dbfx.User(t, "Protected owner", "suspension-owner@example.test")
	ownerMember := dbfx.Member(t, testWorkspaceID, owner, "owner")
	accountAccessRequest(t, testUserID, testWorkspaceID, ownerMember, true).Want(http.StatusForbidden)
	var selfMember string
	dbfx.QueryRow(t, `SELECT id FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID).Scan(&selfMember)
	accountAccessRequest(t, testUserID, testWorkspaceID, selfMember, true).Want(http.StatusBadRequest)
	foreignWorkspace := dbfx.Workspace(t, "Foreign workspace", "suspension-foreign")
	foreignMember := dbfx.Member(t, foreignWorkspace, userID, "member")
	accountAccessRequest(t, testUserID, testWorkspaceID, foreignMember, true).Want(http.StatusNotFound)
	accountAccessRequest(t, testUserID, testWorkspaceID, memberID, true).Want(http.StatusForbidden)
	state, err := testHandler.Queries.GetUserAccountAccess(context.Background(), parseUUID(userID))
	if err != nil || state.AccountDisabledAt.Valid || state.SessionVersion != 0 {
		t.Fatalf("unauthorized change=%+v %v", state, err)
	}
}

// A deliberately stale cache simulates a concurrent fill or failed Redis
// invalidation. It must not resurrect a revoked PAT after account re-enable.
type staleAccountPATCache struct {
	redis.UniversalClient
	user string
}

func (c staleAccountPATCache) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult(c.user, nil)
}

func TestMemberAccountSuspensionRejectsCachedPAT(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	uid := dbfx.User(t, "Cached account", "suspension-cache@example.test")
	member := dbfx.Member(t, testWorkspaceID, uid, "member")
	token, err := auth.GeneratePATToken()
	if err != nil {
		t.Fatal(err)
	}
	patID := dbfx.Insert(t, "personal_access_token", testutil.Cols{"user_id": uid, "name": "account-test", "token_hash": auth.HashToken(token), "token_prefix": token[:12]})
	cache := auth.NewPATCache(staleAccountPATCache{user: uid})
	status := func(daemon bool) int {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		if daemon {
			middleware.DaemonAuth(testHandler.Queries, cache, nil, nil)(handler).ServeHTTP(rec, req)
		} else {
			middleware.Auth(testHandler.Queries, cache, nil, nil)(handler).ServeHTTP(rec, req)
		}
		return rec.Code
	}
	if status(false) != 200 || status(true) != 200 {
		t.Fatal("valid cached PAT denied before suspension")
	}
	accountAccessRequest(t, testUserID, testWorkspaceID, member, true).Want(200)
	if status(false) != 401 || status(true) != 401 {
		t.Fatal("cached PAT bypassed suspension")
	}
	accountAccessRequest(t, testUserID, testWorkspaceID, member, false).Want(200)
	if status(false) != 401 || status(true) != 401 {
		t.Fatal("stale PAT cache revived revoked credential")
	}
	var revoked bool
	dbfx.QueryRow(t, `SELECT revoked FROM personal_access_token WHERE id=$1`, patID).Scan(&revoked)
	if !revoked {
		t.Fatal("PAT not revoked")
	}
	state, err := testHandler.Queries.GetUserAccountAccess(ctx, parseUUID(uid))
	if err != nil || state.SessionVersion != 2 {
		t.Fatalf("state=%+v error=%v", state, err)
	}
}

func TestMemberAccountSuspensionInvalidInputAndOwnerPromotion(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	uid := dbfx.User(t, "Protected account", "suspension-promotion@example.test")
	mid := dbfx.Member(t, testWorkspaceID, uid, "member")
	accountAccessRequest(t, testUserID, testWorkspaceID, mid, true).Want(200)
	req := newRequest("PATCH", "/api/workspaces/"+testWorkspaceID+"/members/"+mid, map[string]any{"role": "owner"})
	req = withURLParams(req, "id", testWorkspaceID, "memberId", mid)
	testutil.Call(t, testHandler.UpdateMember, req).Want(http.StatusBadRequest)
	for _, body := range []map[string]any{{}, {"disabled": "true"}, {"disabled": false, "role": "owner"}} {
		req := newRequest("PATCH", "/api/workspaces/"+testWorkspaceID+"/members/"+mid+"/account-access", body)
		req = withURLParams(req, "id", testWorkspaceID, "memberId", mid)
		testutil.Call(t, testHandler.SetMemberAccountAccess, req).Want(http.StatusBadRequest)
	}
	req = newRequest("PATCH", "/api/workspaces/"+testWorkspaceID+"/members/"+mid+"/account-access", map[string]any{"disabled": false})
	req = withURLParams(req, "id", testWorkspaceID, "memberId", mid)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.SetMemberAccountAccess, req).Want(http.StatusForbidden)
}
