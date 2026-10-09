package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setDshTaskEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MULTICA_TOKEN", "")
	t.Setenv("DSH_SHELL", "1")
	t.Setenv("DSH_MULTICA_TOKEN", "mat_fixture")
	for key, value := range map[string]string{"MULTICA_TASK_ID": "task-test", "MULTICA_AGENT_ID": "agent-test", "MULTICA_WORKSPACE_ID": "workspace-test", "MULTICA_TASK_CONFIG_ROOT": t.TempDir()} {
		t.Setenv(key, value)
		t.Setenv("DSH_"+key, value)
	}
}

func TestDshTaskCredentialReadsAndComments(t *testing.T) {
	setDshTaskEnvironment(t)
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer mat_fixture" {
			t.Error("task authorization missing")
		}
		switch r.URL.Path {
		case "/api/issues/issue-test":
			if r.Method != http.MethodGet {
				t.Error("unexpected read method")
			}
		case "/api/issues/issue-test/comments":
			if r.Method != http.MethodPost {
				t.Error("unexpected comment method")
			}
		default:
			t.Error("unexpected path")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "fixture"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	client, err := newAPIClient(testCmd())
	if err != nil {
		t.Fatal(err)
	}
	if client.TaskID != "task-test" || client.AgentID != "agent-test" {
		t.Fatal("task attribution lost")
	}
	var out map[string]string
	if err := client.GetJSON(context.Background(), "/api/issues/issue-test", &out); err != nil {
		t.Fatal(err)
	}
	if err := client.PostJSON(context.Background(), "/api/issues/issue-test/comments", map[string]string{"body": "hello"}, &out); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("task commands did not reach server")
	}
}

func TestInvalidDshBridgeNeverFallsBackToOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".multica"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".multica", "config.json"), []byte(`{"token":"mul_owner_sentinel"}`), 0600); err != nil {
		t.Fatal(err)
	}
	setDshTaskEnvironment(t)
	t.Setenv("DSH_MULTICA_TASK_ID", "other-task")
	if token := resolveToken(testCmd()); token != "" {
		t.Fatal("invalid bridge fell back to saved credential")
	}
	if _, err := newAPIClient(testCmd()); err == nil {
		t.Fatal("invalid task identity accepted")
	}
	// 2026-10-09 coder(lq): Losing task markers must not enable discovery of the Owner profile either.
	for _, key := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_WORKSPACE_ID", "MULTICA_TASK_CONFIG_ROOT"} {
		t.Setenv(key, "")
	}
	if token := resolveToken(testCmd()); token != "" {
		t.Fatal("marker loss discovered saved Owner credential")
	}
}

func TestDshTaskCredentialStillRejectsExpiredToken(t *testing.T) {
	setDshTaskEnvironment(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	client, err := newAPIClient(testCmd())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PostJSON(context.Background(), "/api/issues/issue-test/comments", map[string]string{"body": "hello"}, nil); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestDshTaskCredentialRepoCheckout(t *testing.T) {
	setDshTaskEnvironment(t)
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer mat_fixture" || r.URL.Path != "/repo/checkout" {
			t.Error("checkout lost task credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": "/work/repo", "branch_name": "agent/test/task"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err := runRepoCheckout(testCmd(), []string{"https://example.test/repo.git"}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("checkout did not reach daemon")
	}
	t.Setenv("DSH_MULTICA_TASK_ID", "other-task")
	if err := runRepoCheckout(testCmd(), []string{"https://example.test/repo.git"}); err == nil || count != 1 {
		t.Fatal("checkout accepted a conflicting task")
	}
}

func TestDshTaskCredentialAuthStatusOmitsToken(t *testing.T) {
	setDshTaskEnvironment(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" || r.Header.Get("Authorization") != "Bearer mat_fixture" {
			t.Error("auth status lost task identity")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "Task Agent", "email": "task@example.test"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	stderr := captureStderr(t)
	err := runAuthStatus(testCmd(), nil)
	stderr.restore()
	output := stderr.read()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Task Agent") || strings.Contains(output, "mat_fixture") || strings.Contains(output, "Token:") {
		t.Fatal("auth status exposed a token or lost task identity")
	}
}
