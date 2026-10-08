package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fakeDshACP = `#!/bin/sh
[ "$*" = "--profile acp" ] || exit 2
while IFS= read -r line; do
 [ -z "$DSH_TEST_RECORD" ] || printf '%s\n' "$line" >> "$DSH_TEST_RECORD"
 id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
 case "$line" in
 *'"method":"initialize"'*)
  printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentInfo":{"version":"0.2.0-test"},"agentCapabilities":{"mcpCapabilities":{"http":true}}}}\n' "$id" ;;
 *'"method":"session/new"'*|*'"method":"session/resume"'*)
  if [ "$DSH_TEST_REJECT" = "1" ]; then
   printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"session is not resumable: old-multica-session"}}\n' "$id"
  else
   sessionField='"sessionId":"test-session",'
   case "$line" in *'"method":"session/resume"'*) sessionField='' ;; esac
   printf '{"jsonrpc":"2.0","id":%s,"result":{%s"configOptions":[{"id":"route","category":"model","type":"select","currentValue":"opaque/model%%2Fid","options":[{"group":"official","name":"Official","options":[{"value":"opaque/model%%2Fid","name":"Official model"}]}]},{"id":"reasoning_effort","category":"thought_level","type":"select","currentValue":"high","options":[{"value":"off","name":"Off"},{"value":"high","name":"High"}]}]}}\n' "$id" "$sessionField"
  fi ;;
 *'"method":"session/set_config_option"'*)
  if [ "$DSH_TEST_CONFIG_FAIL" = "1" ]; then
   printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
  else
   printf '{"jsonrpc":"2.0","id":%s,"result":{"configOptions":[{"id":"route","category":"model","type":"select","currentValue":"opaque/model%%2Fid","options":[{"group":"official","name":"Official","options":[{"value":"opaque/model%%2Fid","name":"Official model"}]}]},{"id":"reasoning_effort","category":"thought_level","type":"select","currentValue":"off","options":[{"value":"off","name":"Off"}]}]}}\n' "$id"
  fi ;;
 *'"method":"session/prompt"'*)
  [ "$DSH_TEST_WAIT" != "1" ] || continue
  printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc1","title":"Read file","status":"pending","rawInput":{"path":"test"}}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"file"}}]}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"DSH_ACP_OK"}}}}'
  printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":20}}}\n' "$id" ;;
 *'"method":"session/close"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; exit 0 ;;
 esac
done
`

func writeDshFixture(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	path := filepath.Join(t.TempDir(), "dsh")
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func runDshFixture(t *testing.T, env map[string]string, opts ExecOptions) (Result, []Message, string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	if env == nil {
		env = map[string]string{}
	}
	env["DSH_TEST_RECORD"] = record
	b, err := New("dsh", Config{ExecutablePath: writeDshFixture(t, fakeDshACP), Logger: slog.Default(), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Cwd == "" {
		opts.Cwd = t.TempDir()
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	s, err := b.Execute(context.Background(), "test prompt", opts)
	if err != nil {
		t.Fatal(err)
	}
	var messages []Message
	for msg := range s.Messages {
		messages = append(messages, msg)
	}
	result := <-s.Result
	requests, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return result, messages, string(requests)
}

func TestNewDshBackend(t *testing.T) {
	b, err := New("dsh", Config{ExecutablePath: "/nonexistent/dsh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*dshBackend); !ok {
		t.Fatalf("unexpected %T", b)
	}
}

func TestDshACPExecution(t *testing.T) {
	result, messages, requests := runDshFixture(t, nil, ExecOptions{Model: "opaque/model%2Fid", ThinkingLevel: "off", McpConfig: json.RawMessage(`{"mcpServers":{"remote":{"type":"http","url":"https://example.test/mcp","headers":{"Authorization":"fixture-only"}}}}`)})
	if result.Status != "completed" || result.Output != "DSH_ACP_OK" || result.SessionID != "test-session" {
		t.Fatalf("result: %+v", result)
	}
	if result.Usage["opaque/model%2Fid"].OutputTokens != 20 {
		t.Fatalf("usage: %+v", result.Usage)
	}
	kinds := map[MessageType]bool{}
	for _, msg := range messages {
		kinds[msg.Type] = true
	}
	for _, kind := range []MessageType{MessageStatus, MessageThinking, MessageToolUse, MessageToolResult, MessageText} {
		if !kinds[kind] {
			t.Errorf("no %s", kind)
		}
	}
	for _, want := range []string{`"method":"session/set_config_option"`, `"configId":"route"`, `"value":"opaque/model%2Fid"`, `"configId":"reasoning_effort"`, `"name":"Authorization"`, `"method":"session/close"`} {
		if !strings.Contains(requests, want) {
			t.Errorf("missing request fragment %s", want)
		}
	}
	if strings.Contains(requests, "session/set_model") {
		t.Fatal("legacy model RPC used")
	}
}

func TestDshConfigFailureDoesNotPrompt(t *testing.T) {
	for _, model := range []string{"unknown", "opaque/model%2Fid"} {
		t.Run(model, func(t *testing.T) {
			result, _, requests := runDshFixture(t, map[string]string{"DSH_TEST_CONFIG_FAIL": "1"}, ExecOptions{Model: model})
			if result.Status != "failed" || strings.Contains(requests, `"method":"session/prompt"`) || result.SessionID != "" {
				t.Fatalf("result: %+v", result)
			}
		})
	}
}

func TestDshResumedConfigFailurePreservesSession(t *testing.T) {
	result, _, requests := runDshFixture(t, map[string]string{"DSH_TEST_CONFIG_FAIL": "1"}, ExecOptions{
		ResumeSessionID: "test-session", Model: "opaque/model%2Fid",
	})
	if result.Status != "failed" || result.SessionID != "test-session" || result.ResumeRejected || strings.Contains(requests, `"method":"session/prompt"`) {
		t.Fatalf("resumed configuration failure lost its existing session: %+v", result)
	}
}

func TestDshACPResume(t *testing.T) {
	result, _, requests := runDshFixture(t, nil, ExecOptions{ResumeSessionID: "test-session"})
	if result.Status != "completed" || !strings.Contains(requests, `"method":"session/resume"`) {
		t.Fatalf("result: %+v", result)
	}
	result, _, requests = runDshFixture(t, map[string]string{"DSH_TEST_REJECT": "1"}, ExecOptions{ResumeSessionID: "old-multica-session"})
	if result.Status != "failed" || !result.ResumeRejected || result.SessionID != "" || strings.Contains(requests, `"method":"session/new"`) {
		t.Fatalf("rejection: %+v", result)
	}
}

func TestDshACPCancellation(t *testing.T) {
	result, _, requests := runDshFixture(t, map[string]string{"DSH_TEST_WAIT": "1"}, ExecOptions{Timeout: 300 * time.Millisecond})
	if result.Status != "timeout" || !strings.Contains(requests, `"method":"session/cancel"`) || !strings.Contains(requests, `"method":"session/close"`) {
		t.Fatalf("result: %+v; requests: %s", result, requests)
	}
}

func TestDiscoverDshModels(t *testing.T) {
	models, err := discoverDshModels(context.Background(), Command{Path: writeDshFixture(t, fakeDshACP)})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "opaque/model%2Fid" || models[0].Thinking == nil || models[0].Thinking.DefaultLevel != "high" {
		t.Fatalf("catalog: %+v", models)
	}
}

func TestDshACPRejectsIncompatibleModelDiscovery(t *testing.T) {
	bin := writeDshFixture(t, strings.Replace(fakeDshACP, `"protocolVersion":1`, `"protocolVersion":99`, 1))
	if _, err := discoverDshModels(context.Background(), Command{Path: bin}); err == nil {
		t.Fatal("incompatible ACP advertised a catalog")
	}
}
