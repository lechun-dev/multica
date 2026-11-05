package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// allowlistFixture seeds an agent owned by a freshly-created user (not the
// workspace owner) so we can drive the "caller is workspace owner / admin
// but not agent owner" branch of UpdateAgent.composio_toolkit_allowlist.
// Returns the agent id and the agent owner's user id; the test's own
// testUserID stays the workspace owner.
func allowlistFixture(t *testing.T) (agentID, agentOwnerID string) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Allowlist Owner', 'allowlist-owner@multica.test')
		RETURNING id
	`).Scan(&agentOwnerID); err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM "user" WHERE email = 'allowlist-owner@multica.test'`)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, agentOwnerID); err != nil {
		t.Fatalf("add owner as workspace member: %v", err)
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, 'allowlist-test-agent', '', 'cloud', '{}'::jsonb,
		        $2, 'workspace', 'public_to', 1, $3, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, handlerTestRuntimeID(t), agentOwnerID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM agent WHERE id = $1`, agentID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_invocation_target (agent_id, target_type, target_id) VALUES ($1, 'workspace', $2)`, agentID, testWorkspaceID); err != nil {
		t.Fatalf("share allowlist fixture: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_invocation_target WHERE agent_id=$1`, agentID)
	})
	return agentID, agentOwnerID
}

func readAllowlistColumn(t *testing.T, agentID string) ([]string, bool) {
	t.Helper()
	var stored []string
	var isNull bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT COALESCE(composio_toolkit_allowlist, '{}'::text[]),
		       composio_toolkit_allowlist IS NULL
		FROM agent WHERE id = $1
	`, agentID).Scan(&stored, &isNull); err != nil {
		t.Fatalf("read allowlist column: %v", err)
	}
	return stored, isNull
}

// TestUpdateAgent_AllowlistRoundtripForOwner is the happy path: the agent
// owner submits a list, then a normalised duplicate, then an empty array,
// then JSON null. The handler must persist the four canonical states
// distinguishable by the dispatch path:
//   - non-empty deduped lowercased TEXT[]
//   - the same value after a noisy second write (idempotent)
//   - empty `{}` after explicit []
//   - NULL after an explicit null
func TestUpdateAgent_AllowlistRoundtripForOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)

	// 1. Owner writes a noisy list — trims/casefolds/dedupes inline.
	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{
			"composio_toolkit_allowlist": []string{" Notion ", "NOTION", "github", ""},
		},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgent owner write: got %d: %s", w.Code, w.Body.String())
	}
	stored, isNull := readAllowlistColumn(t, agentID)
	if isNull {
		t.Fatalf("after owner write, allowlist still NULL")
	}
	if len(stored) != 2 || stored[0] != "notion" || stored[1] != "github" {
		t.Errorf("normalised allowlist = %v; want [notion github]", stored)
	}

	// 2. Owner writes empty array — distinct from NULL on the column.
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"composio_toolkit_allowlist": []string{}},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgent owner empty write: got %d: %s", w.Code, w.Body.String())
	}
	stored, isNull = readAllowlistColumn(t, agentID)
	if isNull {
		t.Fatalf("empty array must persist as empty TEXT[], not NULL")
	}
	if len(stored) != 0 {
		t.Errorf("after empty write, stored = %v; want empty", stored)
	}

	// 3. Owner explicitly nulls the column.
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"composio_toolkit_allowlist": nil},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgent owner null write: got %d: %s", w.Code, w.Body.String())
	}
	_, isNull = readAllowlistColumn(t, agentID)
	if !isNull {
		t.Errorf("explicit null must persist as NULL")
	}
}

func TestUpdateAgent_RejectsWorkspaceOwnerChangingAnotherAgentsAllowlist(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, _ := allowlistFixture(t)

	// Seed an existing allowlist via direct SQL so we can prove the
	// admin's submission did not overwrite it.
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET composio_toolkit_allowlist = $2 WHERE id = $1
	`, agentID, []string{"notion"}); err != nil {
		t.Fatalf("seed existing allowlist: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequest(
		http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"description": "workspace owner update", "composio_toolkit_allowlist": []string{"github", "slack"}},
	), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("UpdateAgent as workspace owner: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	stored, isNull := readAllowlistColumn(t, agentID)
	if isNull || len(stored) != 1 || stored[0] != "notion" {
		t.Fatalf("stored allowlist = %v, isNull = %v; want unchanged [notion]", stored, isNull)
	}
}

func TestGetAgent_AllowlistVisibility(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, true)
	agentID, ownerID := allowlistFixture(t)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET composio_toolkit_allowlist = $2 WHERE id = $1
	`, agentID, []string{"notion", "github"}); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	// Owner sees the list.
	w := httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodGet, "/api/agents/"+agentID, nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetAgent as owner: got %d: %s", w.Code, w.Body.String())
	}
	var ownerResp AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ownerResp); err != nil {
		t.Fatalf("decode owner response: %v", err)
	}
	if len(ownerResp.ComposioToolkitAllowlist) != 2 {
		t.Errorf("owner: allowlist=%v; want length 2", ownerResp.ComposioToolkitAllowlist)
	}
	if ownerResp.ComposioToolkitAllowlistRedacted {
		t.Errorf("owner should not see redacted flag")
	}

	// The workspace owner can inspect the selected toolkit names in the
	// read-only catalog profile. The canonical self endpoint remains private.
	w = httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("GetAgent as workspace owner: got %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.GetOwnerAgentDetail(w, withURLParam(newRequest(http.MethodGet, "/api/agents/catalog/"+agentID, nil), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetOwnerAgentDetail as workspace owner: got %d: %s", w.Code, w.Body.String())
	}
	var detail ownerAgentDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode catalog detail: %v", err)
	}
	if len(detail.Profile.ComposioToolkitAllowlist) != 2 || detail.Profile.ComposioToolkitAllowlistRedacted {
		t.Fatalf("catalog profile should show selected toolkits, got %+v", detail.Profile.ComposioToolkitAllowlist)
	}
}

func TestAgentAllowlistSuppressedWhenComposioFlagDisabled(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withComposioMCPAppsFlag(t, testHandler, false)
	agentID, ownerID := allowlistFixture(t)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET composio_toolkit_allowlist = $2 WHERE id = $1
	`, agentID, []string{"notion"}); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"composio_toolkit_allowlist": []string{"github"}},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgent owner write with flag off: got %d: %s", w.Code, w.Body.String())
	}
	stored, isNull := readAllowlistColumn(t, agentID)
	if isNull || len(stored) != 1 || stored[0] != "notion" {
		t.Fatalf("flag-off write changed allowlist: stored=%v isNull=%v; want unchanged [notion]", stored, isNull)
	}
	var updateResp AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if len(updateResp.ComposioToolkitAllowlist) != 0 || updateResp.ComposioToolkitAllowlistRedacted {
		t.Fatalf("flag-off update response exposed allowlist state: %+v", updateResp)
	}

	w = httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(
		ownerID, http.MethodGet, "/api/agents/"+agentID, nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetAgent owner with flag off: got %d: %s", w.Code, w.Body.String())
	}
	var getResp AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if len(getResp.ComposioToolkitAllowlist) != 0 || getResp.ComposioToolkitAllowlistRedacted {
		t.Fatalf("flag-off get response exposed allowlist state: %+v", getResp)
	}
}

// TestNormaliseComposioToolkitAllowlist_PureFunction exercises the canonical
// normalisation so future refactors don't silently change the persisted
// form (and break the dispatch path's flat slug compare).
func TestNormaliseComposioToolkitAllowlist_PureFunction(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil-in nil-out", nil, nil},
		{"empty-in empty-out", []string{}, []string{}},
		{"trim", []string{"  notion  "}, []string{"notion"}},
		{"lower", []string{"NOTION", "GitHub"}, []string{"notion", "github"}},
		{"dedupe", []string{"notion", "NOTION", "notion"}, []string{"notion"}},
		{"drop empty", []string{"", "   ", "notion"}, []string{"notion"}},
		{"preserve order of first-seen", []string{"notion", "github", "notion"}, []string{"notion", "github"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normaliseComposioToolkitAllowlist(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v; want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v; want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d]=%q; want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
