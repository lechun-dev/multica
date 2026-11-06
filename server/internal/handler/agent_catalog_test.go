package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestListOwnerAgentCatalog(t *testing.T) {
	agentID, agentOwnerID, otherID := privateAgentTestFixture(t)
	runtimeID := handlerTestRuntimeID(t)
	ctx := context.Background()
	const path = "/api/agents/catalog"
	if _, err := testPool.Exec(ctx, `UPDATE agent
		SET instructions = 'private prompt', description = 'Writes reports',
		    avatar_url = 'https://example.com/avatar.png', model = 'test-model', max_concurrent_tasks = 6
		WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	dbfx.Task(t, agentID, testutil.Cols{"status": "running"})
	dbfx.Task(t, agentID, testutil.Cols{"status": "waiting_local_directory"})
	dbfx.Task(t, agentID, testutil.Cols{"status": "completed", "completed_at": time.Now().UTC()})

	w := httptest.NewRecorder()
	testHandler.ListOwnerAgentCatalog(w, newRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("workspace owner: got %d: %s", w.Code, w.Body.String())
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the inaccessible private agent, got %s", w.Body.String())
	}
	entry := entries[0]
	allowed := []string{"id", "name", "owner_id", "created_at", "archived_at", "access_scope", "display"}
	if len(entry) != len(allowed) {
		t.Fatalf("unexpected catalog fields: %v", entry)
	}
	for _, field := range allowed {
		if _, ok := entry[field]; !ok {
			t.Errorf("missing %s", field)
		}
	}
	if string(entry["id"]) != `"`+agentID+`"` || string(entry["owner_id"]) != `"`+agentOwnerID+`"` ||
		string(entry["archived_at"]) != "null" || string(entry["access_scope"]) != `"owner-only"` {
		t.Fatalf("unexpected catalog metadata: %s", w.Body.String())
	}
	var display map[string]json.RawMessage
	if err := json.Unmarshal(entry["display"], &display); err != nil {
		t.Fatal(err)
	}
	wantDisplay := map[string]string{
		"description": `"Writes reports"`, "avatar_url": `"https://example.com/avatar.png"`,
		"runtime_id": `"` + runtimeID + `"`, "model": `"test-model"`,
		"max_concurrent_tasks": "6", "running_count": "1", "queued_count": "1", "run_count": "3",
	}
	if len(display) != len(wantDisplay)+1 {
		t.Fatalf("unexpected catalog display fields: %v", display)
	}
	for field, want := range wantDisplay {
		if got := string(display[field]); got != want {
			t.Errorf("display %s = %s; want %s", field, got, want)
		}
	}
	var activity []AgentActivityBucket
	if err := json.Unmarshal(display["activity"], &activity); err != nil {
		t.Fatal(err)
	}
	if len(activity) != 1 || activity[0].AgentID != agentID || activity[0].TaskCount != 1 || activity[0].CompletedCount != 1 {
		t.Errorf("unexpected catalog activity: %+v", activity)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, suffix string
		handler        http.HandlerFunc
		want           int
	}{
		{"GET", "", testHandler.GetAgent, http.StatusForbidden},
		{"PUT", "", testHandler.UpdateAgent, http.StatusForbidden},
		{"POST", "/restore", testHandler.RestoreAgent, http.StatusForbidden},
		{"GET", "/env", testHandler.GetAgentEnv, http.StatusForbidden},
		{"GET", "/tasks", testHandler.ListAgentTasks, http.StatusOK},
	} {
		w := httptest.NewRecorder()
		url := "/api/agents/" + agentID + tc.suffix
		tc.handler(w, withURLParam(newRequest(tc.method, url, nil), "id", agentID))
		if w.Code != tc.want {
			t.Errorf("workspace owner: %s %s: expected %d, got %d: %s", tc.method, url, tc.want, w.Code, w.Body.String())
		}
	}

	denied := func(label string, req *http.Request, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.ListOwnerAgentCatalog(w, req)
		if w.Code != want {
			t.Fatalf("%s: expected %d, got %d: %s", label, want, w.Code, w.Body.String())
		}
	}
	denied("agent owner without workspace ownership", newRequestAs(agentOwnerID, "GET", path, nil), http.StatusForbidden)
	denied("member", newRequestAs(otherID, "GET", path, nil), http.StatusForbidden)
	if _, err := testPool.Exec(ctx, `UPDATE member SET role = 'admin' WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, otherID); err != nil {
		t.Fatal(err)
	}
	denied("admin", newRequestAs(otherID, "GET", path, nil), http.StatusForbidden)
	taskReq := newRequest("GET", path, nil)
	taskReq.Header.Set("X-Actor-Source", "task_token")
	taskReq.Header.Set("X-Agent-ID", agentID)
	denied("task token bound to workspace owner", taskReq, http.StatusForbidden)
	foreignReq := newRequest("GET", path+"?workspace_id=00000000-0000-0000-0000-000000000001", nil)
	foreignReq.Header.Del("X-Workspace-ID")
	denied("non-member workspace", foreignReq, http.StatusNotFound)
}

func TestGetOwnerAgentDetail(t *testing.T) {
	agentID, _, memberID := privateAgentTestFixture(t)
	ctx := context.Background()
	path := "/api/agents/catalog/" + agentID
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions = 'private prompt', model = 'test-model', thinking_level = 'high', max_concurrent_tasks = 6, runtime_config = '{"gateway":{"token":"gateway-secret"}}', mcp_config = '{"token":"mcp-secret"}', custom_env = '{"TOKEN":"env-secret"}' WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	request := func(req *http.Request, id string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.GetOwnerAgentDetail(w, withURLParam(req, "id", id))
		if w.Code != want {
			t.Fatalf("expected %d, got %d: %s", want, w.Code, w.Body.String())
		}
		return w
	}
	w := request(newRequest("GET", path, nil), agentID, http.StatusOK)
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("unexpected Cache-Control: %q", got)
	}
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"id", "name", "owner_id", "created_at", "archived_at", "access_scope", "description", "updated_at", "model", "thinking_level", "max_concurrent_tasks", "runtime_bound", "profile"}
	if len(detail) != len(allowed) {
		t.Fatalf("unexpected detail field count: got %d, want %d", len(detail), len(allowed))
	}
	for _, field := range allowed {
		if _, ok := detail[field]; !ok {
			t.Errorf("missing %s", field)
		}
	}
	for key, want := range map[string]string{
		"id": `"` + agentID + `"`, "access_scope": `"owner-only"`, "model": `"test-model"`,
		"thinking_level": `"high"`, "max_concurrent_tasks": `6`, "runtime_bound": `true`,
	} {
		if got := string(detail[key]); got != want {
			t.Errorf("%s: expected %s, got %s", key, want, got)
		}
	}
	var profile AgentResponse
	if err := json.Unmarshal(detail["profile"], &profile); err != nil {
		t.Fatal(err)
	}
	if profile.ID != agentID || profile.Instructions != "private prompt" || profile.Model != "test-model" {
		t.Fatal("authorized profile is missing the agent's saved settings")
	}
	if !profile.McpConfigRedacted || len(profile.McpConfig) != 0 && string(profile.McpConfig) != "null" {
		t.Fatal("raw MCP settings should be redacted")
	}
	if !profile.HasCustomEnv || profile.CustomEnvKeyCount != 1 {
		t.Fatal("authorized profile should include environment variable count")
	}
	for _, secret := range []string{"gateway-secret", "mcp-secret", "env-secret", `"custom_env"`} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("profile leaked %q", secret)
		}
	}
	gateway, _ := profile.RuntimeConfig.(map[string]any)["gateway"].(map[string]any)
	if gateway["token"] != runtimeConfigGatewayTokenMask {
		t.Fatal("gateway token is missing its mask")
	}
	if canMemberInvoke(t, agentID, testUserID) {
		t.Error("catalog access must not grant invocation")
	}
	request(newRequestAs(memberID, "GET", path, nil), agentID, http.StatusForbidden)
	if _, err := testPool.Exec(ctx, `UPDATE member SET role = 'admin' WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, memberID); err != nil {
		t.Fatal(err)
	}
	request(newRequestAs(memberID, "GET", path, nil), agentID, http.StatusForbidden)
	taskReq := newRequest("GET", path, nil)
	taskReq.Header.Set("X-Actor-Source", "task_token")
	taskReq.Header.Set("X-Agent-ID", agentID)
	request(taskReq, agentID, http.StatusForbidden)

	otherWorkspace := dbfx.Workspace(t, "Other agent catalog workspace", "other-agent-catalog")
	dbfx.Member(t, otherWorkspace, testUserID, "owner")
	crossWorkspaceReq := newRequest("GET", path, nil)
	crossWorkspaceReq.Header.Set("X-Workspace-ID", otherWorkspace)
	request(crossWorkspaceReq, agentID, http.StatusNotFound)
}

func TestWorkspaceOwnerCanInvokeSharedAgentButCannotEditIt(t *testing.T) {
	agentID, agentOwnerID, _ := privateAgentTestFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent SET permission_mode = 'public_to' WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_invocation_target (agent_id, target_type, target_id) VALUES ($1, 'workspace', $2)`, agentID, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if !canMemberInvoke(t, agentID, testUserID) {
		t.Fatal("workspace sharing should still allow invocation")
	}
	path := "/api/agents/" + agentID
	body := map[string]string{"description": "updated by the agent owner"}
	check := func(req *http.Request, handler http.HandlerFunc, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, withURLParam(req, "id", agentID))
		if w.Code != want {
			t.Errorf("%s %s: expected %d, got %d: %s", req.Method, req.URL, want, w.Code, w.Body.String())
		}
	}
	check(newRequest("GET", path, nil), testHandler.GetAgent, http.StatusForbidden)
	check(newRequest("GET", "/api/agents/catalog/"+agentID, nil), testHandler.GetOwnerAgentDetail, http.StatusOK)
	check(newRequest("PUT", path, body), testHandler.UpdateAgent, http.StatusForbidden)
	check(newRequest("DELETE", path, nil), testHandler.ArchiveAgent, http.StatusForbidden)
	check(newRequest("GET", path+"/env", nil), testHandler.GetAgentEnv, http.StatusForbidden)
	check(newRequest("PUT", path+"/env", nil), testHandler.UpdateAgentEnv, http.StatusForbidden)
	check(newRequest("POST", path+"/mcp-servers", nil), testHandler.AddAgentMcpServer, http.StatusForbidden)
	check(newRequestAs(agentOwnerID, "PUT", path, body), testHandler.UpdateAgent, http.StatusOK)
}
