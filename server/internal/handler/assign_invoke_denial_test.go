package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// assertDenialReason decodes a rejection body and compares its `error` sentence
// to the one the CALLER states it expects, then checks a property that holds for
// every invoke denial: the sentence must not name the target's permission mode.
//
// The expected sentence stays an argument on purpose — the test making the claim
// keeps it. What the helper contributes is the leak scan, which is the same
// property in every case and is exactly what a status-only assertion missed:
// before MUL-6380 these endpoints answered "cannot assign to private agent",
// disclosing the mode to a caller just told they may not use the target, and
// mislabelling a `public_to` agent scoped to specific people as private.
func assertDenialReason(t *testing.T, resp *testutil.Response, want string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	resp.JSON(&body)
	if body.Error != want {
		t.Fatalf("denial reason = %q, want %q", body.Error, want)
	}
	for _, mode := range []string{"private", "public_to", "allow-list"} {
		if strings.Contains(strings.ToLower(body.Error), mode) {
			t.Fatalf("denial reason names the target's permission mode (%q): %q", mode, body.Error)
		}
	}
}

// TestAssignAgent_DenialReasonNamesPermissionNotMode pins the response BODY of
// the assignment invoke gate across every principal that can be refused by it.
// The sibling tests in agent_access_test.go / squad_private_leader_test.go pin
// the status code; a 403 alone does not stop the reason sentence from drifting
// back into describing the target instead of the caller's missing permission.
func TestAssignAgent_DenialReasonNamesPermissionNotMode(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	privateAgentID, agentOwnerID, plainMemberID := privateAgentTestFixture(t)
	const wantAgentReason = "you do not have permission to assign work to this agent"

	assignBody := func(agentID string) map[string]any {
		return map[string]any{
			"title":         "invoke-denial body test",
			"status":        "todo",
			"priority":      "medium",
			"assignee_type": "agent",
			"assignee_id":   agentID,
		}
	}

	t.Run("plain member is refused without naming the mode", func(t *testing.T) {
		resp := testutil.Call(t, testHandler.CreateIssue,
			newRequestAs(plainMemberID, "POST", "/api/issues?workspace_id="+testWorkspaceID, assignBody(privateAgentID)),
		).Want(http.StatusForbidden)
		assertDenialReason(t, resp, wantAgentReason)
	})

	// 2026-10-09 coder(lq): An untrusted agent header must not widen the
	// authenticated member's visibility or invocation authority.
	t.Run("agent header cannot widen member visibility", func(t *testing.T) {
		callerAgentID := createHandlerTestAgent(t, "assign-denial-caller-agent", nil)
		req := newRequestAs(plainMemberID, "POST", "/api/issues?workspace_id="+testWorkspaceID, assignBody(privateAgentID))
		req.Header.Set("X-Agent-ID", callerAgentID)
		resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusForbidden)
		assertDenialReason(t, resp, wantAgentReason)
	})

	// A `public_to` agent whose allow-list names someone else. This is the case
	// the old wording got factually wrong: the agent is not private, so calling
	// it that told the caller something untrue about the target.
	t.Run("public_to non-target member is refused without calling the agent private", func(t *testing.T) {
		scopedAgentID := dbfx.Agent(t, "assign-denial-scoped-agent", handlerTestRuntimeID(t), testutil.Cols{
			"visibility":      "private",
			"permission_mode": "public_to",
			"owner_id":        agentOwnerID,
			"instructions":    "",
			"custom_env":      testutil.Raw("'{}'::jsonb"),
			"custom_args":     testutil.Raw("'[]'::jsonb"),
		})
		// Granted to the agent owner only — the plain member is deliberately
		// NOT on the list.
		dbfx.Exec(t, `
			INSERT INTO agent_invocation_target (agent_id, target_type, target_id)
			VALUES ($1, 'member', $2)
			ON CONFLICT (agent_id, target_type, target_id) DO NOTHING
		`, scopedAgentID, agentOwnerID)
		// No FK/cascade by project rule, so the grant outlives the agent row
		// dbfx deletes — drop it explicitly.
		dbfx.Cleanup(t, `DELETE FROM agent_invocation_target WHERE agent_id = $1`, scopedAgentID)

		resp := testutil.Call(t, testHandler.CreateIssue,
			newRequestAs(plainMemberID, "POST", "/api/issues?workspace_id="+testWorkspaceID, assignBody(scopedAgentID)),
		).Want(http.StatusForbidden)
		assertDenialReason(t, resp, wantAgentReason)
	})

	// The squad branch has its own sentence, and must not disclose the leader's
	// mode either — "this squad" is all the caller gets.
	t.Run("private-leader squad is refused without naming the leader's mode", func(t *testing.T) {
		squadID := dbfx.Squad(t, "Assign Denial Private Leader", privateAgentID)
		resp := testutil.Call(t, testHandler.CreateIssue,
			newRequestAs(plainMemberID, "POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
				"title":         "invoke-denial squad body test",
				"assignee_type": "squad",
				"assignee_id":   squadID,
			}),
		).Want(http.StatusForbidden)
		assertDenialReason(t, resp, "you do not have permission to assign work to this squad")
	})
}

func TestCreateIssue_DelegationCannotBypassPrivateAgentAccess(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	targetID, targetOwnerID, sourceOwnerID := privateAgentTestFixture(t)
	adminID := dbfx.User(t, "LC-3211 workspace administrator", "lc3211-admin@multica.test")
	dbfx.Member(t, testWorkspaceID, adminID, "admin")
	sourceRuntimeID := dbfx.Runtime(t, "LC-3211 source runtime", testutil.Cols{"owner_id": sourceOwnerID})
	sourceAgentID := dbfx.Agent(t, "LC-3211 delegating agent", sourceRuntimeID, testutil.Cols{
		"owner_id":        sourceOwnerID,
		"visibility":      "workspace",
		"permission_mode": "public_to",
	})
	dbfx.InsertNoID(t, "agent_invocation_target", testutil.Cols{
		"agent_id":    sourceAgentID,
		"target_type": "workspace",
		"target_id":   testWorkspaceID,
	}, "agent_id=$1", sourceAgentID)

	// 2026-11-05 coder(lq): LC-3211: the task token identifies the source
	// runtime owner, but delegation authority comes from the stored run originator.
	createIssue := middleware.Auth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(testHandler.CreateIssue))
	for _, tc := range []struct {
		name, originatorID string
		wantStatus         int
	}{
		{"admin cannot delegate to another member's private agent", adminID, http.StatusForbidden},
		{"workspace owner cannot delegate to another member's private agent", testUserID, http.StatusForbidden},
		{"agent owner can delegate to their own private agent", targetOwnerID, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourceTaskID := dbfx.Task(t, sourceAgentID, testutil.Cols{
				"runtime_id":          sourceRuntimeID,
				"status":              "running",
				"originator_user_id":  tc.originatorID,
				"accountable_user_id": tc.originatorID,
			})
			token, err := auth.GenerateAgentTaskToken()
			if err != nil {
				t.Fatal(err)
			}
			dbfx.Insert(t, "task_token", testutil.Cols{
				"token_hash":   auth.HashToken(token),
				"task_id":      sourceTaskID,
				"agent_id":     sourceAgentID,
				"workspace_id": testWorkspaceID,
				"user_id":      sourceOwnerID,
				"expires_at":   testutil.Raw("now() + interval '1 hour'"),
			})

			title := "LC-3211 delegated issue " + uuid.NewString()
			dbfx.Cleanup(t, `DELETE FROM issue WHERE workspace_id=$1 AND title=$2`, testWorkspaceID, title)
			dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, targetID)
			req := newRequest(http.MethodPost, "/api/issues", map[string]any{
				"title":         title,
				"assignee_type": "agent",
				"assignee_id":   targetID,
			})
			req.Header.Set("Authorization", "Bearer "+token)
			resp := testutil.Call(t, createIssue.ServeHTTP, req).Want(tc.wantStatus)
			if tc.wantStatus == http.StatusForbidden {
				assertDenialReason(t, resp, "you do not have permission to assign work to this agent")
				if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1 AND title=$2`, testWorkspaceID, title); n != 0 {
					t.Fatalf("denied delegation created %d issues", n)
				}
				if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, targetID); n != 0 {
					t.Fatalf("denied delegation created %d queue entries", n)
				}
				return
			}

			var issue IssueResponse
			resp.JSON(&issue)
			if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND issue_id=$2 AND status='queued' AND originator_user_id=$3`, targetID, issue.ID, tc.originatorID); n != 1 {
				t.Fatalf("authorized delegation queued %d tasks for the owner, want 1", n)
			}
		})
	}
}
