package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDWSIdentityMatchesPrefersUnionID(t *testing.T) {
	message := &DingTalkPersonalMessage{
		SenderUnionID:    "union-author",
		SenderDingUserID: "user-author",
		SenderCorpID:     "corp-a",
	}
	if !dwsIdentityMatches(message, "union-author", "different-user", "corp-b") {
		t.Fatal("matching unionId should be authoritative")
	}
	if dwsIdentityMatches(message, "different-union", "user-author", "corp-a") {
		t.Fatal("a different unionId must not fall back to the corp-scoped userId")
	}
}

func TestDWSIdentityMatchesFallsBackWhenDWSOmitsUnionID(t *testing.T) {
	message := &DingTalkPersonalMessage{
		SenderUnionID:    "union-author",
		SenderDingUserID: "user-author",
		SenderCorpID:     "corp-a",
	}
	if !dwsIdentityMatches(message, "", "user-author", "corp-a") {
		t.Fatal("missing DWS unionId should fall back to matching corp-scoped userId")
	}
	if dwsIdentityMatches(message, "", "user-author", "corp-b") {
		t.Fatal("fallback must still reject a different corporation")
	}
}

func TestDWSIdentityMatchesFallsBackToCorpAndUser(t *testing.T) {
	message := &DingTalkPersonalMessage{SenderDingUserID: "user-author", SenderCorpID: "corp-a"}
	if !dwsIdentityMatches(message, "", "user-author", "corp-a") {
		t.Fatal("matching corpId and userId should be accepted")
	}
	if dwsIdentityMatches(message, "", "user-author", "corp-b") {
		t.Fatal("a different corpId must be rejected")
	}
	if dwsIdentityMatches(message, "", "user-author", "") {
		t.Fatal("a missing actual corpId must be rejected when the Multica identity has one")
	}
	if dwsIdentityMatches(&DingTalkPersonalMessage{SenderDingUserID: "user-author"}, "", "user-author", "corp-a") {
		t.Fatal("userId-only fallback must be rejected because DingTalk userIds are corp-scoped")
	}
}

func TestRunDWSCommandRejectsJSONErrorWithZeroExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	fakePath := filepath.Join(t.TempDir(), "dws")
	if err := os.WriteFile(fakePath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"success\":false,\"error\":{\"code\":\"Forbidden\",\"message\":\"permission denied\"}}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := runDWSCommand(context.Background(), fakePath, "chat", "message", "send", "--format", "json")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("zero-exit JSON failure was not surfaced: %v", err)
	}
}

func TestRunDWSMessageDeliveryUsesFakeExecutableAndConfirmsStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	temp := t.TempDir()
	logPath := filepath.Join(temp, "commands.log")
	fakePath := filepath.Join(temp, "dws")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DWS_FAKE_LOG"
case "$1 $2 $3" in
  "auth status --format")
    printf '%s\n' '{"success":true,"authenticated":true}'
    ;;
  "contact user get-self")
    printf '%s\n' '{"success":true,"result":{"unionId":"union-author","userId":"user-author","corpId":"corp-a"}}'
    ;;
  "chat message send")
    printf '%s\n' '{"success":true,"result":{"openTaskId":"task-123"}}'
    ;;
  "chat message query-send-status")
    printf '%s\n' '{"success":true,"result":{"status":"SUCCESS","openMessageId":"message-456"}}'
    ;;
  *)
    printf '%s\n' '{"error":{"message":"unexpected fake command"}}'
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakePath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_DWS_PATH", fakePath)
	t.Setenv("DWS_FAKE_LOG", logPath)

	var submitted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"status":"submitted"`) && strings.Contains(string(body), `"dws_open_task_id":"task-123"`) {
			submitted = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	d := &Daemon{
		cfg:    Config{DaemonID: "daemon-test"},
		client: NewClient(server.URL),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	result := d.runDWSMessageDelivery(context.Background(), &DingTalkPersonalMessage{
		ID:                  "message-row",
		SenderUnionID:       "union-author",
		SenderDingUserID:    "user-author",
		SenderCorpID:        "corp-a",
		RecipientDingUserID: "user-recipient",
		Markdown:            "## hello\n\nbody",
		IdempotencyKey:      "mention-key",
	})
	if result.Status != "delivered" || result.DWSOpenTaskID != "task-123" || result.DWSOpenMessageID != "message-456" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !submitted {
		t.Fatal("openTaskId checkpoint was not reported before status polling")
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	commandText := string(commands)
	for _, required := range []string{
		"chat message send",
		"--user user-recipient",
		"--title MissionOS 通知",
		"--idempotency-key mention-key",
		"chat message query-send-status --open-task-id task-123",
	} {
		if !strings.Contains(commandText, required) {
			t.Errorf("fake DWS command log missing %q:\n%s", required, commandText)
		}
	}
	if strings.Contains(commandText, "--ai-tag") {
		t.Fatalf("personal message must use DWS's default AI tag: %s", commandText)
	}
}

func TestRunDWSMessageDeliveryFallsBackToExactOpenDingTalkID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	temp := t.TempDir()
	logPath := filepath.Join(temp, "commands.log")
	fakePath := filepath.Join(temp, "dws")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DWS_FAKE_LOG"
case "$1 $2 $3" in
  "auth status --format")
    printf '%s\n' '{"success":true,"authenticated":true}'
    ;;
  "contact user get-self")
    printf '%s\n' '{"success":true,"result":{"unionId":"union-author","userId":"user-author","corpId":"corp-a"}}'
    ;;
  "contact user search")
    printf '%s\n' '{"success":true,"result":[{"userId":"decoy","openDingTalkId":"open-decoy"},{"userId":"user-recipient","openDingTalkId":"open-recipient"}]}'
    ;;
  "chat message send")
    case " $* " in
      *" --user user-recipient "*)
        printf '%s\n' '{"error":{"message":"cannot resolve --user user-recipient to openDingTalkId"}}'
        exit 1
        ;;
      *" --open-dingtalk-id open-recipient "*)
        printf '%s\n' '{"success":true,"result":{"openTaskId":"task-fallback"}}'
        ;;
      *)
        printf '%s\n' '{"error":{"message":"unexpected recipient"}}'
        exit 1
        ;;
    esac
    ;;
  "chat message query-send-status")
    printf '%s\n' '{"success":true,"result":{"status":"SUCCESS","openMessageId":"message-fallback"}}'
    ;;
  *)
    printf '%s\n' '{"error":{"message":"unexpected fake command"}}'
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakePath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_DWS_PATH", fakePath)
	t.Setenv("DWS_FAKE_LOG", logPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	d := &Daemon{
		cfg:    Config{DaemonID: "daemon-test"},
		client: NewClient(server.URL),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	result := d.runDWSMessageDelivery(context.Background(), &DingTalkPersonalMessage{
		ID:                  "message-row",
		SenderUnionID:       "union-author",
		SenderDingUserID:    "user-author",
		SenderCorpID:        "corp-a",
		RecipientDingUserID: "user-recipient",
		Markdown:            "## hello\n\nbody",
		IdempotencyKey:      "mention-key",
	})
	if result.Status != "delivered" || result.DWSOpenTaskID != "task-fallback" || result.DWSOpenMessageID != "message-fallback" {
		t.Fatalf("unexpected result: %+v", result)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	commandText := string(commands)
	for _, required := range []string{
		"chat message send --user user-recipient",
		"contact user search --keyword user-recipient --format json",
		"chat message send --open-dingtalk-id open-recipient",
	} {
		if !strings.Contains(commandText, required) {
			t.Errorf("fake DWS command log missing %q:\n%s", required, commandText)
		}
	}
	if count := strings.Count(commandText, "--idempotency-key mention-key"); count != 2 {
		t.Fatalf("idempotency key appeared %d times, want once per send attempt:\n%s", count, commandText)
	}
}

func TestRunDWSMessageDeliveryDoesNotUseInexactRecipientMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	temp := t.TempDir()
	logPath := filepath.Join(temp, "commands.log")
	fakePath := filepath.Join(temp, "dws")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DWS_FAKE_LOG"
case "$1 $2 $3" in
  "auth status --format")
    printf '%s\n' '{"success":true,"authenticated":true}'
    ;;
  "contact user get-self")
    printf '%s\n' '{"success":true,"result":{"unionId":"union-author"}}'
    ;;
  "contact user search")
    printf '%s\n' '{"success":true,"result":[{"userId":"different-user","openDingTalkId":"open-wrong"}]}'
    ;;
  "chat message send")
    printf '%s\n' '{"error":{"message":"cannot resolve --user user-recipient to openDingTalkId"}}'
    exit 1
    ;;
  *)
    printf '%s\n' '{"error":{"message":"unexpected fake command"}}'
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakePath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_DWS_PATH", fakePath)
	t.Setenv("DWS_FAKE_LOG", logPath)

	d := &Daemon{}
	result := d.runDWSMessageDelivery(context.Background(), &DingTalkPersonalMessage{
		SenderUnionID:       "union-author",
		RecipientDingUserID: "user-recipient",
		IdempotencyKey:      "mention-key",
	})
	if result.Status != "retry" || result.ErrorCode != "dws_recipient_identity_unresolved" {
		t.Fatalf("unexpected result: %+v", result)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "--open-dingtalk-id") {
		t.Fatalf("inexact recipient identity was used:\n%s", commands)
	}
}

func TestRunDWSMessageDeliveryQueuesWhenFakeDWSIsLoggedOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	fakePath := filepath.Join(t.TempDir(), "dws")
	if err := os.WriteFile(fakePath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"success\":true,\"authenticated\":false}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_DWS_PATH", fakePath)
	d := &Daemon{}
	result := d.runDWSMessageDelivery(context.Background(), &DingTalkPersonalMessage{})
	if result.Status != "waiting_for_dws_login" || result.ErrorCode != "dws_not_logged_in" {
		t.Fatalf("unexpected logged-out result: %+v", result)
	}
}
