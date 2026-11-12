package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The task detail renders the assignee and creator from the task payload. It used
// to resolve them from the browser's workspace-wide agent directory, which cannot
// name an agent that is not in that list and rendered "Unknown Agent" instead.
func TestIssueDetailProjectsAgentActorNames(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "Actor names runtime")
	agentName := "Actor names agent"
	agentID := dbfx.Agent(t, agentName, runtimeID)
	issueID := dbfx.Issue(t, "Actor names task", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"creator_type":  "agent",
		"creator_id":    agentID,
	})

	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID)
	var body struct {
		AssigneeName *string `json:"assignee_name"`
		CreatorName  *string `json:"creator_name"`
	}
	testutil.Call(t, testHandler.GetIssue, req).Want(http.StatusOK).JSON(&body)

	if body.AssigneeName == nil || *body.AssigneeName != agentName {
		t.Fatalf("assignee_name = %v, want %q", body.AssigneeName, agentName)
	}
	if body.CreatorName == nil || *body.CreatorName != agentName {
		t.Fatalf("creator_name = %v, want %q", body.CreatorName, agentName)
	}
}
