package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestAgentIssueAccessRechecksAgentUseAfterRevocation(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	originatorID := dbfx.User(t, "Ongoing authorization originator", fmt.Sprintf("ongoing-auth-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, originatorID, "member")
	issueID := dbfx.Issue(t, "Ongoing authorization source")
	if err := upsertProjectlessIssueAccessGrant(context.Background(), testPool, issueID, originatorID, projectauth.TaskManager); err != nil {
		t.Fatalf("grant task manager: %v", err)
	}
	var agentID, runtimeID string
	dbfx.QueryRow(t, `SELECT id, runtime_id FROM agent WHERE workspace_id=$1 LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "originator_user_id": originatorID, "accountable_user_id": originatorID,
	})
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	req := newRequestAs(originatorID, http.MethodGet, "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	if allowed, reason := testHandler.issueProjectAllowed(req, issue, projectauth.View); !allowed {
		t.Fatalf("before revoke: allowed=false reason=%q", reason)
	}

	dbfx.Exec(t, `DELETE FROM projectauth_issue_access_grants WHERE issue_id=$1 AND subject_id=$2`, issueID, originatorID)
	if allowed, reason := testHandler.issueProjectAllowed(req, issue, projectauth.View); allowed || reason != "forbidden" {
		t.Fatalf("after revoke: allowed=%v reason=%q, want false/forbidden", allowed, reason)
	}
}

func TestRestrictedAgentTaskRequiresExplicitTargetPermissionToPublish(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)

	originatorID := dbfx.User(t, "Restricted publish originator", fmt.Sprintf("restricted-publish-%s@multica.test", t.Name()))
	dbfx.Member(t, testWorkspaceID, originatorID, "member")
	parentProjectID := dbfx.Project(t, "Broader parent project")
	childProjectID := dbfx.Project(t, "Restricted child project")
	parentIssueID := dbfx.Issue(t, "Broader parent issue", testutil.Cols{"project_id": parentProjectID})
	childIssueID := dbfx.Issue(t, "Restricted child issue", testutil.Cols{
		"project_id": childProjectID, "parent_issue_id": parentIssueID,
	})
	dbfx.Insert(t, "projectauth_access_grants", testutil.Cols{
		"workspace_id": testWorkspaceID, "project_id": parentProjectID,
		"subject_type": "user", "subject_id": originatorID, "role_key": "member", "source": "manual", "granted_by": testUserID,
	})
	if err := upsertIssueAccessGrant(context.Background(), testPool, childIssueID, childProjectID, originatorID, projectauth.TaskManager); err != nil {
		t.Fatalf("grant child task manager: %v", err)
	}
	dbfx.InsertNoID(t, "projectauth_issue_policies", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": childIssueID,
		"project_access_mode": "restricted", "created_by": testUserID, "updated_by": testUserID,
	}, "workspace_id=$1 AND issue_id=$2", testWorkspaceID, childIssueID)

	var agentID, runtimeID string
	dbfx.QueryRow(t, `SELECT id, runtime_id FROM agent WHERE workspace_id=$1 LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": childIssueID, "originator_user_id": originatorID, "accountable_user_id": originatorID,
	})
	post := func(content string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequestAs(originatorID, http.MethodPost, "/api/issues/"+parentIssueID+"/comments", map[string]any{"content": content})
		req = withURLParam(req, "id", parentIssueID)
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		testHandler.CreateComment(w, req)
		return w
	}

	denied := post("must not escape through inherited project access")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("project-only target access: got %d: %s", denied.Code, denied.Body.String())
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND content LIKE 'must not escape%'`, parentIssueID); got != 0 {
		t.Fatalf("denied cross-task comment count=%d, want 0", got)
	}

	if err := upsertIssueAccessGrant(context.Background(), testPool, parentIssueID, parentProjectID, originatorID, projectauth.TaskMember); err != nil {
		t.Fatalf("grant explicit target task member: %v", err)
	}
	allowed := post("explicitly authorized handoff")
	if allowed.Code != http.StatusCreated {
		t.Fatalf("task-explicit target access: got %d: %s", allowed.Code, allowed.Body.String())
	}
}
