//go:build agentintegration

package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 2026-10-08 coder(lq): TestDshRealRuntimeSmoke is opt-in because it calls the configured DeepSeek
// model and consumes API quota. It exercises the complete Multica backend,
// official DSH ACP profile, model provider, and terminal-result path.
func TestDshRealRuntimeSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to run the DSH integration smoke")
	}
	path := os.Getenv("MULTICA_DSH_PATH")
	if path == "" {
		t.Fatal("MULTICA_DSH_PATH is required")
	}
	if _, err := InspectDshACP(context.Background(), Command{Path: path}); err != nil {
		t.Fatalf("official ACP inspection failed: %v", err)
	}
	models, err := discoverDshModels(context.Background(), Command{Path: path})
	if err != nil || len(models) == 0 {
		t.Fatalf("model discovery failed: %v", err)
	}
	selected := models[0]
	for _, model := range models {
		if model.Default {
			selected = model
		}
	}
	level := ""
	if selected.Thinking != nil {
		for _, option := range selected.Thinking.SupportedLevels {
			if option.Value == "off" {
				level = option.Value
			}
		}
	}
	b, err := New("dsh", Config{ExecutablePath: path, TaskID: "dsh-real-smoke", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cwd := t.TempDir()
	session, err := b.Execute(ctx, "Remember the code word PINEAPPLE. Reply with exactly DSH_MULTICA_OK and nothing else.", ExecOptions{
		Cwd: cwd, Model: selected.ID, ThinkingLevel: level,
		Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("DSH smoke failed: status=%q error=%q", result.Status, result.Error)
	}
	if strings.TrimSpace(result.Output) != "DSH_MULTICA_OK" {
		t.Fatalf("unexpected DSH output: %q", result.Output)
	}
	if result.SessionID == "" {
		t.Fatal("DSH smoke returned no session ID")
	}

	resumed, err := b.Execute(ctx, "Reply with exactly the code word from the previous turn and nothing else.", ExecOptions{
		Cwd: cwd, Model: selected.ID, ThinkingLevel: level,
		ResumeSessionID: result.SessionID, Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range resumed.Messages {
	}
	resumeResult := <-resumed.Result
	if resumeResult.Status != "completed" {
		t.Fatalf("DSH resume smoke failed: status=%q error=%q", resumeResult.Status, resumeResult.Error)
	}
	if strings.TrimSpace(resumeResult.Output) != "PINEAPPLE" {
		t.Fatalf("DSH resume lost conversation context: %q", resumeResult.Output)
	}
	if resumeResult.SessionID != result.SessionID {
		t.Fatalf("DSH resume changed session ID: %q -> %q", result.SessionID, resumeResult.SessionID)
	}
}

// 2026-10-08 coder(lq): Exercise the official App's config and persistence without model quota or user-state writes.
func TestDshOfficialACPConfigurationSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("explicit real-agent opt-in required")
	}
	path := os.Getenv("MULTICA_DSH_PATH")
	if path == "" {
		t.Fatal("MULTICA_DSH_PATH is required")
	}
	home, cwd := t.TempDir(), t.TempDir()
	open := func() (*hermesClient, func()) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := Command{Path: path}.exec(ctx, dshLaunchArgs()...)
		cmd.Env = replaceEnvValue(replaceEnvValue(os.Environ(), "DSH_HOME", home), "DSH_TELEMETRY_DISABLED", "1")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cmd.Stderr = io.Discard
		if err := startOwnedProcessTree(cmd, slog.Default()); err != nil {
			cancel()
			t.Fatal(err)
		}
		client := &hermesClient{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, stdin: stdin, pending: map[int]*pendingRPC{}, pendingTools: map[string]*pendingToolCall{}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			scanner := newAgentStreamScanner(stdout)
			for scanner.Scan() {
				client.handleLine(scanner.Text())
			}
			client.closeAllPending(io.EOF)
		}()
		cleanup := func() {
			stdin.Close()
			signalProcessGroup(cmd, syscall.SIGKILL)
			cmd.Wait()
			releaseProcessGroup(cmd)
			cancel()
			stdout.Close()
			<-done
		}
		init, err := client.request(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
		if err != nil || !strings.Contains(string(init), `"protocolVersion":1`) {
			cleanup()
			t.Fatal("official ACP initialize failed")
		}
		return client, cleanup
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, cleanup := open()
	state, err := client.request(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		cleanup()
		t.Fatal("official session/new failed")
	}
	sid := extractACPSessionID(state)
	models := parseACPConfigOptionModels(state)
	if sid == "" || len(models) == 0 {
		cleanup()
		t.Fatal("official session omitted model catalog or ID")
	}
	selected := models[0].ID
	for _, model := range models {
		if model.Default {
			selected = model.ID
		}
	}
	state, err = applyDshConfig(ctx, client.request, sid, state, "model", selected)
	if err == nil {
		state, err = applyDshConfig(ctx, client.request, sid, state, "thought_level", "off")
	}
	if err != nil {
		cleanup()
		t.Fatal("official model/effort confirmation failed")
	}
	_, err = client.request(ctx, "session/close", map[string]any{"sessionId": sid})
	cleanup()
	if err != nil {
		t.Fatal("official session close failed")
	}
	client, cleanup = open()
	defer cleanup()
	resumed, err := client.request(ctx, "session/resume", map[string]any{"cwd": cwd, "mcpServers": []any{}, "sessionId": sid})
	resumedID, _ := resolveResumedSessionID(sid, resumed)
	if err != nil || resumedID != sid {
		t.Fatalf("official cross-process resume failed: %s", sanitizeAgentDiagnostic(fmt.Sprint(err)))
	}
	if _, err := applyDshConfig(ctx, client.request, sid, resumed, "thought_level", "off"); err != nil {
		t.Fatal("official resumed-session configuration failed")
	}
	if _, err := client.request(ctx, "session/close", map[string]any{"sessionId": sid}); err != nil {
		t.Fatal("official resumed session close failed")
	}
	_, err = client.request(ctx, "session/resume", map[string]any{"cwd": cwd, "mcpServers": []any{}, "sessionId": "missionos-nonexistent-session"})
	if !isACPResumeRejected(err) {
		t.Fatal("official stale-session rejection was not classified")
	}
	t.Logf("official ACP initialization, %d advertised models, configuration, close and cross-process resume verified", len(models))
}
