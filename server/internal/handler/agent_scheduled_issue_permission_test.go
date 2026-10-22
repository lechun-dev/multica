package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-12 coder(lq): Scheduled runs keep originator attribution empty;
// their ongoing task operations must use the same human as enqueue/claim.
func TestScheduledAgentIssueOperationsUseAccountableHuman(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	enableProjectAuthForTest(t)
	accountable := dbfx.User(t, "Scheduled accountable", "scheduled-accountable@example.test")
	originator := dbfx.User(t, "Scheduled originator", "scheduled-originator@example.test")
	dbfx.Member(t, testWorkspaceID, accountable, "member")
	dbfx.Member(t, testWorkspaceID, originator, "member")
	projectID := dbfx.Project(t, "Scheduled permission project")
	var agentID, runtimeID string
	dbfx.QueryRow(t, `SELECT id, runtime_id FROM agent WHERE workspace_id=$1 LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID)
	issueID := dbfx.Issue(t, "Scheduled permission source", testutil.Cols{
		"project_id": projectID, "creator_type": "agent", "creator_id": agentID,
		"assignee_type": "agent", "assignee_id": agentID,
	})
	if err := upsertIssueAccessGrant(context.Background(), testPool, issueID, projectID, accountable, projectauth.TaskManager); err != nil {
		t.Fatal(err)
	}
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "originator_user_id": nil,
		"accountable_user_id": accountable, "originator_source": "trigger_owner",
	})
	actorID := agentID
	request := func(method, suffix string, body any) *http.Request {
		req := newRequestAs(testUserID, method, "/api/issues/"+issueID+suffix, body)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", actorID)
		req.Header.Set("X-Task-ID", taskID)
		return withURLParam(req, "id", issueID)
	}
	operations := func(t *testing.T, want int) {
		t.Helper()
		testutil.Call(t, testHandler.GetIssue, request(http.MethodGet, "", nil)).Want(want)
		testutil.Call(t, testHandler.ListComments, request(http.MethodGet, "/comments", nil)).Want(want)
		commentStatus := want
		if want == http.StatusOK {
			commentStatus = http.StatusCreated
		}
		testutil.Call(t, testHandler.CreateComment, request(http.MethodPost, "/comments", map[string]any{
			"content": "Scheduled operation " + t.Name(),
		})).Want(commentStatus)
		testutil.Call(t, testHandler.UpdateIssue, request(http.MethodPut, "", map[string]any{"status": "done"})).Want(want)
	}
	t.Run("scheduled operations", func(t *testing.T) {
		operations(t, http.StatusOK)
		var originatorRemainsEmpty bool
		dbfx.QueryRow(t, `SELECT originator_user_id IS NULL FROM agent_task_queue WHERE id=$1`, taskID).Scan(&originatorRemainsEmpty)
		if !originatorRemainsEmpty {
			t.Fatal("authorization fallback rewrote scheduled attribution")
		}
	})
	t.Run("manual originator cannot borrow authenticated owner authority", func(t *testing.T) {
		// 2026-10-12 coder(lq): Valid attributed rows require accountable to
		// equal originator; do not manufacture an impossible attribution pair.
		dbfx.Exec(t, `UPDATE agent_task_queue SET originator_user_id=$2, accountable_user_id=$2 WHERE id=$1`, taskID, originator)
		t.Cleanup(func() {
			dbfx.Exec(t, `UPDATE agent_task_queue SET originator_user_id=NULL, accountable_user_id=$2 WHERE id=$1`, taskID, accountable)
		})
		operations(t, http.StatusForbidden)
	})
	t.Run("task viewer cannot continue agent operations", func(t *testing.T) {
		// 2026-10-12 coder(lq): Grants are additive; remove Manager before
		// installing Viewer so this exercises actual AgentUse revocation.
		dbfx.Exec(t, `DELETE FROM projectauth_access_grants WHERE issue_id=$1 AND subject_id=$2`, issueID, accountable)
		t.Cleanup(func() {
			dbfx.Exec(t, `DELETE FROM projectauth_access_grants WHERE issue_id=$1 AND subject_id=$2`, issueID, accountable)
			if err := upsertIssueAccessGrant(context.Background(), testPool, issueID, projectID, accountable, projectauth.TaskManager); err != nil {
				t.Fatal(err)
			}
		})
		if err := upsertIssueAccessGrant(context.Background(), testPool, issueID, projectID, accountable, projectauth.TaskViewer); err != nil {
			t.Fatal(err)
		}
		humanRead := newRequestAs(accountable, http.MethodGet, "/api/issues/"+issueID, nil)
		humanRead.Header.Set("X-Workspace-ID", testWorkspaceID)
		testutil.Call(t, testHandler.GetIssue, withURLParam(humanRead, "id", issueID)).Want(http.StatusOK)
		operations(t, http.StatusForbidden)
	})
	t.Run("task must belong to authenticated agent", func(t *testing.T) {
		actorID = dbfx.Agent(t, "Unrelated scheduled agent", runtimeID)
		t.Cleanup(func() { actorID = agentID })
		operations(t, http.StatusNotFound)
	})
	t.Run("source task cannot cross workspace", func(t *testing.T) {
		otherWorkspace := dbfx.Workspace(t, "Other scheduled workspace", "other-scheduled-workspace")
		otherIssue := dbfx.Issue(t, "Other scheduled source", testutil.Cols{"workspace_id": otherWorkspace})
		dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=$2 WHERE id=$1`, taskID, otherIssue)
		t.Cleanup(func() {
			dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=$2 WHERE id=$1`, taskID, issueID)
		})
		operations(t, http.StatusNotFound)
	})
	t.Run("revocation takes effect during execution", func(t *testing.T) {
		dbfx.Exec(t, `DELETE FROM projectauth_access_grants WHERE issue_id=$1 AND subject_id=$2`, issueID, accountable)
		t.Cleanup(func() {
			if err := upsertIssueAccessGrant(context.Background(), testPool, issueID, projectID, accountable, projectauth.TaskManager); err != nil {
				t.Fatal(err)
			}
		})
		operations(t, http.StatusForbidden)
	})
	t.Run("no human cannot borrow authenticated owner authority", func(t *testing.T) {
		dbfx.Exec(t, `UPDATE agent_task_queue SET accountable_user_id=NULL WHERE id=$1`, taskID)
		t.Cleanup(func() {
			dbfx.Exec(t, `UPDATE agent_task_queue SET accountable_user_id=$2 WHERE id=$1`, taskID, accountable)
		})
		operations(t, http.StatusNotFound)
	})
	t.Run("removed accountable membership", func(t *testing.T) {
		dbfx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, accountable)
		operations(t, http.StatusNotFound)
	})
}
