package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// 2026-11-24 coder(lq): A run_only autopilot task carries no source issue, so
// the source-issue requirement in issuePermissionSubject must not apply. Its
// issue operations authorize as the durable task's accountable human; the task
// token's X-User-ID names the runtime owner and must never become the subject.
func TestIssueLessRunAuthorizesProjectIssueOperations(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	runtimeID := handlerTestRuntimeID(t)
	projectID := dbfx.Project(t, "Issue-less automation project")
	issueID := dbfx.Issue(t, "Issue-less automation target", testutil.Cols{"project_id": projectID})
	agentID := dbfx.Agent(t, "Issue-less automation agent", runtimeID)
	runtimeOwner := dbfx.User(t, "Runtime owner", fmt.Sprintf("runtime-owner-%d@multica.test", time.Now().UnixNano()))
	dbfx.Member(t, testWorkspaceID, runtimeOwner, "member")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id":          runtimeID,
		"status":              "running",
		"accountable_user_id": testUserID,
	})

	agentRequest := func(method, path string, body any) *http.Request {
		req := newRequest(method, path, body)
		// The task token stamps the runtime owner here; authorization has to
		// resolve the accountable human from the task instead.
		req.Header.Set("X-User-ID", runtimeOwner)
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		req.Header.Set("X-Actor-Source", "task_token")
		return req
	}

	t.Run("read", func(t *testing.T) {
		req := withURLParam(agentRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID)
		if w := callIssueAccessEndpoint(testHandler.GetIssue, req); w.Code != http.StatusOK {
			t.Fatalf("agent read of a project issue = %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("update", func(t *testing.T) {
		req := withURLParam(agentRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
			"title": "Issue-less automation target (updated)",
		}), "id", issueID)
		if w := callIssueAccessEndpoint(testHandler.UpdateIssue, req); w.Code != http.StatusOK {
			t.Fatalf("agent update of a project issue = %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("create in project", func(t *testing.T) {
		req := agentRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title":      "Issue-less automation child",
			"project_id": projectID,
		})
		if w := callIssueAccessEndpoint(testHandler.CreateIssue, req); w.Code != http.StatusCreated {
			t.Fatalf("agent create in a project = %d: %s", w.Code, w.Body.String())
		}
	})
}

// A task without an originator or accountable human has nobody to speak for,
// so the issue stays inaccessible rather than inheriting the runtime owner.
func TestIssueLessRunWithoutAuthorizationHumanIsDenied(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	runtimeID := handlerTestRuntimeID(t)
	projectID := dbfx.Project(t, "Issue-less unattributed project")
	issueID := dbfx.Issue(t, "Issue-less unattributed target", testutil.Cols{"project_id": projectID})
	agentID := dbfx.Agent(t, "Issue-less unattributed agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"status":     "running",
	})

	req := newRequest(http.MethodGet, "/api/issues/"+issueID, nil)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Actor-Source", "task_token")
	req = withURLParam(req, "id", issueID)
	if w := callIssueAccessEndpoint(testHandler.GetIssue, req); w.Code != http.StatusNotFound {
		t.Fatalf("unattributed run read = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// The accountable human still needs project access: an agent run may not widen
// what the human it speaks for can see.
func TestIssueLessRunOutsideProjectAccessIsDenied(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	runtimeID := handlerTestRuntimeID(t)
	projectID := dbfx.Project(t, "Issue-less private project")
	issueID := dbfx.Issue(t, "Issue-less private target", testutil.Cols{"project_id": projectID})
	agentID := dbfx.Agent(t, "Issue-less outsider agent", runtimeID)
	outsider := dbfx.User(t, "Issue-less outsider", fmt.Sprintf("issue-less-outsider-%d@multica.test", time.Now().UnixNano()))
	dbfx.Member(t, testWorkspaceID, outsider, "member")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id":          runtimeID,
		"status":              "running",
		"accountable_user_id": outsider,
	})

	req := newRequest(http.MethodGet, "/api/issues/"+issueID, nil)
	req.Header.Set("X-User-ID", outsider)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Actor-Source", "task_token")
	req = withURLParam(req, "id", issueID)
	// Issue-level project denials answer 403 with the stable permission code;
	// only an unresolvable task (no human to speak for) is reported as 404.
	if w := callIssueAccessEndpoint(testHandler.GetIssue, req); w.Code != http.StatusForbidden {
		t.Fatalf("outsider run read = %d, want 403: %s", w.Code, w.Body.String())
	}
}
