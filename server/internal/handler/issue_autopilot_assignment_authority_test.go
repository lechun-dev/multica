package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func autopilotChildIssueRequest(t *testing.T, assigneeType, assigneeID, parentIssueID, status, actorAgentID, taskID string) *http.Request {
	t.Helper()

	r := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "autopilot private-assignee child " + t.Name(),
		"status":          status,
		"priority":        "low",
		"assignee_type":   assigneeType,
		"assignee_id":     assigneeID,
		"parent_issue_id": parentIssueID,
		"allow_duplicate": true,
	})
	if actorAgentID != "" {
		r.Header.Set("X-Agent-ID", actorAgentID)
	}
	if taskID != "" {
		r.Header.Set("X-Task-ID", taskID)
	}
	return r
}

func cleanupAutopilotChildIssue(t *testing.T, issueID string) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
}

func TestCreateIssue_AutopilotLeaderAssignsPrivateWorker(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("verified lineage cannot borrow creator authority for backlog child", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")
		parentIssueID := uuidToString(fx.Issue.ID)

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, parentIssueID, "backlog", fx.LeaderAgentID, fx.LeaderTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("verified lineage cannot borrow creator authority for active child", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "todo", fx.LeaderAgentID, fx.LeaderTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("verified lineage cannot borrow creator authority for squad child", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")
		squadID := dbfx.Squad(t, "Autopilot Private Leader Squad", workerID)

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "squad", squadID, uuidToString(fx.Issue.ID), "todo", fx.LeaderAgentID, fx.LeaderTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("creator without invoke rights is denied", func(t *testing.T) {
		workerID, _, plainMemberID := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, plainMemberID, "autopilot")

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "backlog", fx.LeaderAgentID, fx.LeaderTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("real originator takes precedence over autopilot creator", func(t *testing.T) {
		workerID, ownerID, plainMemberID := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")
		dbfx.Exec(t, `
			UPDATE agent_task_queue
			SET originator_user_id = $1, accountable_user_id = $1, originator_source = 'direct_human'
			WHERE id = $2
		`, plainMemberID, fx.LeaderTaskID)

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "backlog", fx.LeaderAgentID, fx.LeaderTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("missing task lineage is denied", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "backlog", fx.LeaderAgentID, ""),
		).Want(http.StatusForbidden)
	})

	t.Run("task actor mismatch is denied", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")
		workerTaskID := seedTaskOnIssue(t, workerID, uuidToString(fx.Issue.ID), fx.RuntimeID)

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "backlog", fx.LeaderAgentID, workerTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("task bound to another issue is denied", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")
		otherIssueID := seedBareIssue(t, fx.LeaderAgentID)
		otherTaskID := seedTaskOnIssue(t, fx.LeaderAgentID, otherIssueID, fx.RuntimeID)

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "backlog", fx.LeaderAgentID, otherTaskID),
		).Want(http.StatusForbidden)
	})

	t.Run("cross-workspace parent is rejected before assignee authorization", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "autopilot")
		squadID := dbfx.Squad(t, "Autopilot Cross-Workspace Squad", workerID)
		foreignWorkspaceID := dbfx.Workspace(t, "Autopilot Foreign Parent", "autopilot-foreign-parent-"+workerID[:8])
		foreignParentID := dbfx.Issue(t, "Autopilot foreign parent", testutil.Cols{
			"workspace_id": foreignWorkspaceID,
			"number":       1,
		})

		for _, target := range []struct {
			name, assigneeType, assigneeID string
		}{
			{name: "agent", assigneeType: "agent", assigneeID: workerID},
			{name: "squad", assigneeType: "squad", assigneeID: squadID},
		} {
			t.Run(target.name, func(t *testing.T) {
				resp := testutil.Call(t, testHandler.CreateIssue,
					autopilotChildIssueRequest(t, target.assigneeType, target.assigneeID, foreignParentID, "backlog", fx.LeaderAgentID, fx.LeaderTaskID),
				).Want(http.StatusBadRequest)
				if !strings.Contains(resp.Text(), "parent issue not found in this workspace") {
					t.Fatalf("cross-workspace parent rejection = %q, want workspace boundary error", resp.Text())
				}

				title := "autopilot private-assignee child " + t.Name()
				if count := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title); count != 0 {
					t.Fatalf("cross-workspace parent rejection created %d issue rows", count)
				}
			})
		}
	})

	t.Run("non-autopilot parent is denied", func(t *testing.T) {
		workerID, ownerID, _ := privateAgentTestFixture(t)
		fx := newAutopilotDelegationFixture(t, workerID, ownerID, "")

		testutil.Call(t, testHandler.CreateIssue,
			autopilotChildIssueRequest(t, "agent", workerID, uuidToString(fx.Issue.ID), "backlog", fx.LeaderAgentID, fx.LeaderTaskID),
		).Want(http.StatusForbidden)
	})
}
