package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func enableProjectAuthForTest(t *testing.T) {
	t.Helper()
	previous := testHandler.ProjectAuth
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })
}

func projectRoleForTest(t *testing.T, projectID, userID string) string {
	t.Helper()
	var role string
	dbfx.QueryRow(t, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID).Scan(&role)
	return role
}

func issueMemberPermissionsForTest(t *testing.T, issueID, userID string) map[string]bool {
	t.Helper()
	rows, err := testPool.Query(t.Context(), `
		SELECT permission FROM issue_permissions WHERE issue_id = $1 AND user_id = $2`, issueID, userID)
	if err != nil {
		t.Fatalf("query task member permissions: %v", err)
	}
	defer rows.Close()
	permissions := map[string]bool{}
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			t.Fatalf("scan task member permission: %v", err)
		}
		permissions[permission] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read task member permissions: %v", err)
	}
	return permissions
}

// 2026-08-27 coder(lq): Project lead updates and project descriptions are
// authorization events, so they must grant owner and viewer roles atomically.
func TestProjectUpdatePromotesMemberLeadAndMention(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	leadID := dbfx.User(t, "Project lead", fmt.Sprintf("project-lead-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, leadID, "member")
	viewerID := dbfx.User(t, "Project viewer", fmt.Sprintf("project-viewer-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, viewerID, "member")
	projectID := dbfx.Project(t, "Automatic project roles")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/projects/"+projectID+"?workspace_id="+testWorkspaceID, map[string]any{
		"lead_type":   "member",
		"lead_id":     leadID,
		"description": fmt.Sprintf("Please review [@Viewer](mention://member/%s)", viewerID),
	})
	req = withURLParam(req, "id", projectID)
	testHandler.UpdateProject(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateProject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := projectRoleForTest(t, projectID, leadID); got != "owner" {
		t.Fatalf("project lead role = %q, want owner", got)
	}
	if got := projectRoleForTest(t, projectID, viewerID); got != "viewer" {
		t.Fatalf("mentioned project member role = %q, want viewer", got)
	}
}

// 2026-08-27 coder(lq): Assigning a project task to a workspace member must
// grant project member access, including the ordinary non-description update.
func TestIssueUpdatePromotesMemberAssignee(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	assigneeID := dbfx.User(t, "Issue assignee", fmt.Sprintf("issue-assignee-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, assigneeID, "member")
	projectID := dbfx.Project(t, "Automatic assignee role")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)
	issueID := dbfx.Issue(t, "Assign me", testutil.Cols{"project_id": projectID})

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/issues/"+issueID+"?workspace_id="+testWorkspaceID, map[string]any{
		"assignee_type": "member",
		"assignee_id":   assigneeID,
	})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := projectRoleForTest(t, projectID, assigneeID); got != "member" {
		t.Fatalf("issue assignee project role = %q, want member", got)
	}
}

// 2026-08-27 coder(lq): Assignment promotion is monotonic: a new assignee
// receives member access when absent or viewer, while manager/owner grants
// made by a project owner remain untouched.
func TestIssueUpdateAssigneePromotionPreservesStrongerProjectRoles(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	projectID := dbfx.Project(t, "Monotonic assignee roles")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)
	issueID := dbfx.Issue(t, "Assign with role promotion", testutil.Cols{"project_id": projectID})

	cases := []struct {
		name        string
		initialRole string
		wantRole    string
	}{
		{name: "no grant", wantRole: "member"},
		{name: "viewer grant", initialRole: "viewer", wantRole: "member"},
		{name: "member grant", initialRole: "member", wantRole: "member"},
		{name: "manager grant", initialRole: "manager", wantRole: "manager"},
		{name: "owner grant", initialRole: "owner", wantRole: "owner"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assigneeID := dbfx.User(t, "Role assignee", fmt.Sprintf("role-assignee-%s-%d@multica.test", t.Name(), i))
			dbfx.Member(t, testWorkspaceID, assigneeID, "member")
			if tc.initialRole != "" {
				dbfx.InsertNoID(t, "project_members", testutil.Cols{
					"project_id": projectID,
					"user_id":    assigneeID,
					"role":       tc.initialRole,
				}, "project_id = $1 AND user_id = $2", projectID, assigneeID)
			}

			w := httptest.NewRecorder()
			req := newRequest(http.MethodPut, "/api/issues/"+issueID+"?workspace_id="+testWorkspaceID, map[string]any{
				"assignee_type": "member",
				"assignee_id":   assigneeID,
			})
			req = withURLParam(req, "id", issueID)
			testHandler.UpdateIssue(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("UpdateIssue: expected 200, got %d: %s", w.Code, w.Body.String())
			}
			if got := projectRoleForTest(t, projectID, assigneeID); got != tc.wantRole {
				t.Fatalf("issue assignee project role = %q, want %q", got, tc.wantRole)
			}
		})
	}
}

// 2026-09-05 coder(lq): Task creation keeps assignee inheritance separate from
// task-member mention grants; mentions do not create project membership.
func TestCreateIssuePromotesAssigneeAndMentionedMember(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	assigneeID := dbfx.User(t, "Created issue assignee", fmt.Sprintf("created-issue-assignee-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, assigneeID, "member")
	viewerID := dbfx.User(t, "Created issue viewer", fmt.Sprintf("created-issue-viewer-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, viewerID, "member")
	projectID := dbfx.Project(t, "Created issue automatic roles")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)

	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "New task with inherited project access",
		"project_id":    projectID,
		"assignee_type": "member",
		"assignee_id":   assigneeID,
		"description":   fmt.Sprintf("Please review [@Viewer](mention://member/%s)", viewerID),
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created issue: %v", err)
	}
	if got := projectRoleForTest(t, projectID, assigneeID); got != "member" {
		t.Fatalf("created issue assignee project role = %q, want member", got)
	}
	if got := projectRoleForTest(t, projectID, viewerID); got != "" {
		t.Fatalf("created issue mention unexpectedly became project role %q", got)
	}
	permissions := issueMemberPermissionsForTest(t, created.ID, viewerID)
	for _, permission := range []string{"project.view", "project.issue.comment"} {
		if !permissions[permission] {
			t.Fatalf("created issue mention missing task permission %q: %v", permission, permissions)
		}
	}
}

// 2026-09-05 coder(lq): A human mentioned in a task comment becomes a member
// of that task only and can reply without joining the project.
func TestCreateCommentPromotesMentionedMemberViewer(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	viewerID := dbfx.User(t, "Comment viewer", fmt.Sprintf("comment-viewer-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, viewerID, "member")
	projectID := dbfx.Project(t, "Automatic comment role")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)
	issueID := dbfx.Issue(t, "Mention a reviewer", testutil.Cols{"project_id": projectID})

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments?workspace_id="+testWorkspaceID, map[string]any{
		"content": fmt.Sprintf("Please review [@Viewer](mention://member/%s)", viewerID),
	})
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := projectRoleForTest(t, projectID, viewerID); got != "" {
		t.Fatalf("comment mention unexpectedly became project role %q", got)
	}
	permissions := issueMemberPermissionsForTest(t, issueID, viewerID)
	for _, permission := range []string{"project.view", "project.issue.comment"} {
		if !permissions[permission] {
			t.Fatalf("comment mention missing task permission %q: %v", permission, permissions)
		}
	}
}

// 2026-09-05 coder(lq): Editing an existing task comment can introduce a new
// task member mention without changing project membership.
func TestUpdateCommentPromotesMentionedMemberViewer(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	viewerID := dbfx.User(t, "Edited comment viewer", fmt.Sprintf("edited-comment-viewer-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, viewerID, "member")
	projectID := dbfx.Project(t, "Edited comment automatic role")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)
	issueID := dbfx.Issue(t, "Mention a reviewer by editing", testutil.Cols{"project_id": projectID})
	commentID := dbfx.Comment(t, issueID, "Review needed")
	var initialCount int
	dbfx.QueryRow(t, `SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, viewerID).Scan(&initialCount)
	if initialCount != 0 {
		t.Fatalf("mentioned member unexpectedly had project access before edit")
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/comments/"+commentID+"?workspace_id="+testWorkspaceID, map[string]any{
		"content": fmt.Sprintf("Please review [@Viewer](mention://member/%s)", viewerID),
	})
	req = withURLParam(req, "commentId", commentID)
	testHandler.UpdateComment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateComment: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := projectRoleForTest(t, projectID, viewerID); got != "" {
		t.Fatalf("edited comment unexpectedly became project role %q", got)
	}
	permissions := issueMemberPermissionsForTest(t, issueID, viewerID)
	for _, permission := range []string{"project.view", "project.issue.comment"} {
		if !permissions[permission] {
			t.Fatalf("edited comment mention missing task permission %q: %v", permission, permissions)
		}
	}
}

// 2026-08-27 coder(lq): Plugin content writes are another official task
// description entrypoint and must inherit the same mention-based viewer rule.
func TestPatchPluginIssuePromotesMentionedMemberViewer(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	viewerID := dbfx.User(t, "Plugin viewer", fmt.Sprintf("plugin-viewer-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, viewerID, "member")
	projectID := dbfx.Project(t, "Plugin mention role")
	dbfx.InsertNoID(t, "project_members", testutil.Cols{
		"project_id": projectID,
		"user_id":    testUserID,
		"role":       "owner",
	}, "project_id = $1 AND user_id = $2", projectID, testUserID)
	issueID := dbfx.Issue(t, "Plugin mention", testutil.Cols{"project_id": projectID})
	installationID := installPluginForAction(t, []string{"issues:read", "issues:write"})

	w := httptest.NewRecorder()
	req := pluginActionRequest(http.MethodPatch, "/v1/issues/"+issueID, installationID, map[string]any{
		"description": fmt.Sprintf("Please review [@Viewer](mention://member/%s)", viewerID),
	}, map[string]string{"issue_ref": issueID})
	testHandler.PatchPluginIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PatchPluginIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := projectRoleForTest(t, projectID, viewerID); got != "" {
		t.Fatalf("plugin mention unexpectedly became project role %q", got)
	}
	permissions := issueMemberPermissionsForTest(t, issueID, viewerID)
	for _, permission := range []string{"project.view", "project.issue.comment"} {
		if !permissions[permission] {
			t.Fatalf("plugin mention missing task permission %q: %v", permission, permissions)
		}
	}
}
