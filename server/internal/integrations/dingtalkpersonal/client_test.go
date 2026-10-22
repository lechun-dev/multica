package dingtalkpersonal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCallToolSendsDWSHeadersAndArguments(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("x-user-access-token"); got != "access-token" {
			t.Errorf("x-user-access-token = %q", got)
		}
		var request struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Method != "tools/call" || request.Params.Name != "example_tool" {
			t.Fatalf("unexpected request: %#v", request)
		}
		if request.Params.Arguments["value"] != "hello" {
			t.Fatalf("arguments = %#v", request.Params.Arguments)
		}
		writeRPCResult(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client()}
	result, err := service.callTool(context.Background(), server.URL, "access-token", "example_tool", map[string]any{"value": "hello"})
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	if result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestResolveRecipientRequiresExactUniqueIdentity(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeRPCResult(t, w, map[string]any{
			"result": []any{
				map[string]any{"userId": "other", "openDingTalkId": "ignore"},
				map[string]any{"userId": "ding-user-1", "openDingTalkId": "open-user-1"},
			},
		})
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client(), contactEndpoint: server.URL}
	openID, err := service.resolveRecipient(context.Background(), "token", "ding-user-1")
	if err != nil {
		t.Fatalf("resolveRecipient: %v", err)
	}
	if openID != "open-user-1" {
		t.Fatalf("openID = %q", openID)
	}
}

func TestSendUsesMarkdownAndIdempotencyKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		arguments := request.Params.Arguments
		if arguments["receiverOpenDingTalkId"] != "open-recipient" || arguments["msgType"] != "markdown" || arguments["uuid"] != "message-key" {
			t.Fatalf("arguments = %#v", arguments)
		}
		var content map[string]string
		if err := json.Unmarshal([]byte(arguments["content"].(string)), &content); err != nil {
			t.Fatalf("decode content: %v", err)
		}
		if content["text"] != "**message**" {
			t.Fatalf("content = %#v", content)
		}
		writeRPCResult(t, w, map[string]any{"openTaskId": "task-1"})
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client(), chatEndpoint: server.URL}
	taskID, messageID, err := service.send(context.Background(), "token", "open-recipient", "**message**", "message-key")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if taskID != "task-1" || messageID != "" {
		t.Fatalf("taskID = %q, messageID = %q", taskID, messageID)
	}
}

func TestEncodePersonalMessageContentPreservesContentWithinLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
	}{
		{name: "short markdown", text: "**message**"},
		{
			name: "exact Unicode character limit",
			text: strings.Repeat("a", personalMessageTextRuneLimit),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := encodePersonalMessageContent(test.text)
			want := encodeContent(personalMessageTitle, test.text)
			if got != want {
				t.Fatalf("content changed within limit: got %d bytes, want %d bytes", len(got), len(want))
			}
			if len(got) > personalMessageContentByteLimit {
				t.Fatalf("content is %d bytes, limit is %d", len(got), personalMessageContentByteLimit)
			}
			var decoded map[string]string
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("decode content: %v", err)
			}
			if utf8.RuneCountInString(decoded["text"]) > personalMessageTextRuneLimit {
				t.Fatalf("text is %d runes, limit is %d", utf8.RuneCountInString(decoded["text"]), personalMessageTextRuneLimit)
			}
		})
	}
}

func TestEncodePersonalMessageContentTruncatesOversizedTextWithEllipsis(t *testing.T) {
	t.Parallel()

	content := encodePersonalMessageContent(strings.Repeat("a", personalMessageTextRuneLimit+1))

	var decoded map[string]string
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if decoded["title"] != personalMessageTitle {
		t.Fatalf("title = %q", decoded["title"])
	}
	if !strings.HasSuffix(decoded["text"], personalMessageTruncationMarker) {
		t.Fatalf("text does not end in ellipsis: %q", decoded["text"])
	}
	if strings.Count(decoded["text"], personalMessageTruncationMarker) != 1 {
		t.Fatalf("text must contain exactly one ellipsis: %q", decoded["text"])
	}
	if got := utf8.RuneCountInString(decoded["text"]); got != personalMessageTextRuneLimit {
		t.Fatalf("text is %d runes, want %d", got, personalMessageTextRuneLimit)
	}
	if got, want := strings.TrimSuffix(decoded["text"], personalMessageTruncationMarker), strings.Repeat("a", personalMessageTextRuneLimit-1); got != want {
		t.Fatalf("truncated prefix length = %d, want %d", utf8.RuneCountInString(got), utf8.RuneCountInString(want))
	}
}

func TestEncodePersonalMessageContentKeepsUTF8ValidAndMeasuresJSONEscapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
	}{
		{name: "Chinese and emoji", text: strings.Repeat("中文🙂", 5000)},
		{
			name: "JSON escapes",
			text: strings.Repeat("<", 800) + strings.Repeat("\"\\\n", 66) + "\"",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			content := encodePersonalMessageContent(test.text)
			if len(content) > personalMessageContentByteLimit {
				t.Fatalf("content is %d bytes, limit is %d", len(content), personalMessageContentByteLimit)
			}
			if !utf8.ValidString(content) {
				t.Fatal("serialized content is not valid UTF-8")
			}

			var decoded map[string]string
			if err := json.Unmarshal([]byte(content), &decoded); err != nil {
				t.Fatalf("decode content: %v", err)
			}
			if !utf8.ValidString(decoded["text"]) {
				t.Fatal("truncated text is not valid UTF-8")
			}
			if got := utf8.RuneCountInString(decoded["text"]); got > personalMessageTextRuneLimit {
				t.Fatalf("text is %d runes, limit is %d", got, personalMessageTextRuneLimit)
			}
			if !strings.HasSuffix(decoded["text"], personalMessageTruncationMarker) {
				t.Fatalf("text does not end in ellipsis: %q", decoded["text"])
			}
			if test.name == "JSON escapes" && utf8.RuneCountInString(decoded["text"]) >= utf8.RuneCountInString(test.text) {
				t.Fatalf("JSON-expanded text was not truncated: got %d runes from %d", utf8.RuneCountInString(decoded["text"]), utf8.RuneCountInString(test.text))
			}
		})
	}
}

func TestEncodePersonalMessageContentPreservesTaskFooter(t *testing.T) {
	t.Parallel()

	footer := "\n\n---\n\n[打开任务并回复](https://multica.test/issues/LC-71)（来源：[LC-71 · 子任务测试](https://multica.test/issues/LC-71)）"
	for _, body := range []string{
		strings.Repeat("a", 2000),
		strings.Repeat("中文🙂", 2000),
		strings.Repeat("<\"\\\n", 500),
		"正文中的分隔线\n\n---\n\n" + strings.Repeat("正文", 1000),
	} {
		content := encodePersonalMessageContent(body + footer)
		var decoded map[string]string
		if err := json.Unmarshal([]byte(content), &decoded); err != nil {
			t.Fatalf("decode content: %v", err)
		}
		text := decoded["text"]
		if !strings.HasSuffix(text, personalMessageTruncationMarker+footer) {
			t.Fatalf("task footer was truncated: %q", text)
		}
		prefix := strings.TrimSuffix(text, personalMessageTruncationMarker+footer)
		if !strings.HasPrefix(body, prefix) || prefix == body {
			t.Fatalf("body is not a truncated prefix: %q", prefix)
		}
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) > personalMessageTextRuneLimit || len(content) > personalMessageContentByteLimit {
			t.Fatalf("invalid or oversized content: %d runes, %d bytes", utf8.RuneCountInString(text), len(content))
		}
		// The retained prefix should use the available budget, including JSON escapes.
		nextRune := []rune(strings.TrimPrefix(body, prefix))[0]
		next := prefix + string(nextRune) + personalMessageTruncationMarker + footer
		if utf8.RuneCountInString(next) <= personalMessageTextRuneLimit && len(encodeContent(personalMessageTitle, next)) <= personalMessageContentByteLimit {
			t.Fatal("truncation left space for another body character")
		}
	}

	for _, body := range []string{"短正文🙂", strings.Repeat("a", personalMessageTextRuneLimit-utf8.RuneCountInString(footer))} {
		text := body + footer
		if got := encodePersonalMessageContentDetails(text); got.truncated || got.serialized != encodeContent(personalMessageTitle, text) {
			t.Fatal("message within the limit changed")
		}
	}

	// An ordinary separator in a comment is not an application-generated footer.
	text := strings.Repeat("正文", 1000) + "\n\n---\n\n普通正文"
	if got := encodePersonalMessageContent(text); strings.Contains(got, "普通正文") {
		t.Fatal("ordinary body suffix was incorrectly reserved as a footer")
	}
}

func TestEncodePersonalMessageContentReservesFooterBudget(t *testing.T) {
	t.Parallel()
	for _, footer := range []string{
		"\n\n---\n\n（来源：LC-71 · 任务）",
		"\n\n---\n\n来源：[LC-71](https://multica.test/issues/LC-71)\n\n[打开任务并回复](https://multica.test/issues/LC-71)",
		"\n\n---\n\n[打开任务并回复](https://multica.test/?q=" + strings.Repeat("<", 500) + ")",
		"\n\n---\n\n（来源：" + strings.Repeat("a", 983) + "）",
	} {
		got := encodePersonalMessageContentDetails(strings.Repeat("正文", 1000) + footer)
		if got.err != nil {
			t.Fatalf("unexpected footer error: %v", got.err)
		}
		var decoded map[string]string
		if err := json.Unmarshal([]byte(got.serialized), &decoded); err != nil {
			t.Fatal(err)
		}
		if !got.truncated || !strings.HasSuffix(decoded["text"], personalMessageTruncationMarker+footer) || len(got.serialized) > personalMessageContentByteLimit || utf8.RuneCountInString(decoded["text"]) > personalMessageTextRuneLimit {
			t.Fatalf("footer budget not respected: %q", decoded["text"])
		}
	}
}

func TestSendRejectsFooterThatCannotFitWithoutCallingProvider(t *testing.T) {
	t.Parallel()
	for _, title := range []string{strings.Repeat("a", 1000), strings.Repeat("<", 700)} {
		// No HTTP client is configured: an attempted provider call would panic.
		service := &Service{}
		_, _, err := service.send(context.Background(), "token", "recipient", "正文\n\n---\n\n（来源："+title+"）", "key")
		var deliveryErr *DeliveryError
		if !errors.As(err, &deliveryErr) || deliveryErr.Code != "message_footer_too_long" || deliveryErr.Retryable {
			t.Fatalf("error = %v, want non-retryable footer length error", err)
		}
	}
}

func TestQuerySendStatusPreservesFailureDetails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeRPCResult(t, w, map[string]any{
			"status":       "FAILED",
			"errorCode":    "invalidParameter",
			"errorMessage": " content is too long \n please shorten it ",
		})
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client(), imEndpoint: server.URL}
	messageID, pending, err := service.querySendStatus(context.Background(), "token", "task-failed-1")
	if messageID != "" || pending {
		t.Fatalf("messageID = %q, pending = %v", messageID, pending)
	}
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %v", err)
	}
	if deliveryErr.Code != "dingtalk_delivery_failed" || deliveryErr.ProviderStatus != "FAILED" || deliveryErr.ProviderCode != "invalidParameter" || deliveryErr.ProviderReason != "content is too long please shorten it" || deliveryErr.OpenTaskID != "task-failed-1" {
		t.Fatalf("delivery error = %#v", deliveryErr)
	}
	if !strings.Contains(deliveryErr.Message, "content is too long please shorten it") {
		t.Fatalf("message = %q", deliveryErr.Message)
	}
}

func TestSendBoundsQueuedOversizedMarkdownAtDeliveryTime(t *testing.T) {
	t.Parallel()
	footer := "\n\n---\n\n[打开任务并回复](https://multica.test/issues/LC-71)（来源：LC-71 · 子任务测试）"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		arguments := request.Params.Arguments
		content, ok := arguments["content"].(string)
		if !ok {
			t.Fatalf("content = %#v", arguments["content"])
		}
		if len(content) > personalMessageContentByteLimit {
			t.Fatalf("content is %d bytes, limit is %d", len(content), personalMessageContentByteLimit)
		}
		if arguments["receiverOpenDingTalkId"] != "open-recipient" || arguments["msgType"] != "markdown" || arguments["uuid"] != "message-key" {
			t.Fatalf("arguments = %#v", arguments)
		}
		var decoded map[string]string
		if err := json.Unmarshal([]byte(content), &decoded); err != nil {
			t.Fatalf("decode content: %v", err)
		}
		if decoded["title"] != personalMessageTitle || !strings.HasSuffix(decoded["text"], personalMessageTruncationMarker+footer) {
			t.Fatalf("content = %#v", decoded)
		}
		if utf8.RuneCountInString(decoded["text"]) > personalMessageTextRuneLimit {
			t.Fatal("queued text exceeds the character limit")
		}
		writeRPCResult(t, w, map[string]any{"openTaskId": "task-oversized"})
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client(), chatEndpoint: server.URL}
	taskID, _, err := service.send(context.Background(), "token", "open-recipient", strings.Repeat("超长消息🙂", 5000)+footer, "message-key")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if taskID != "task-oversized" {
		t.Fatalf("taskID = %q", taskID)
	}
}

func TestCallToolClassifiesAuthorizationAndPermissionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		code         string
		authRequired bool
		retryable    bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: "authorization_expired", authRequired: true},
		{name: "forbidden", status: http.StatusForbidden, code: "permission_denied"},
		{name: "rate limited", status: http.StatusTooManyRequests, code: "dingtalk_temporarily_unavailable", retryable: true},
		{name: "server error", status: http.StatusBadGateway, code: "dingtalk_temporarily_unavailable", retryable: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream error", test.status)
			}))
			defer server.Close()
			service := &Service{httpClient: server.Client()}
			_, err := service.callTool(context.Background(), server.URL, "token", "tool", nil)
			var deliveryErr *DeliveryError
			if !errors.As(err, &deliveryErr) {
				t.Fatalf("error = %v", err)
			}
			if deliveryErr.Code != test.code || deliveryErr.AuthRequired != test.authRequired || deliveryErr.Retryable != test.retryable {
				t.Fatalf("delivery error = %#v", deliveryErr)
			}
		})
	}
}

func TestCallToolClassifiesBusinessPermissionErrorWithoutCallingItLogout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      3,
			"result": map[string]any{
				"isError": true,
				"structuredContent": map[string]any{
					"errorMessage": "permission denied for send_personal_message",
				},
			},
		})
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client()}
	_, err := service.callTool(context.Background(), server.URL, "valid-token", "send_personal_message", nil)
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %v", err)
	}
	if deliveryErr.Code != "permission_denied" || deliveryErr.AuthRequired {
		t.Fatalf("delivery error = %#v", deliveryErr)
	}
}

func writeRPCResult(t *testing.T, w http.ResponseWriter, content map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"result": map[string]any{
			"structuredContent": content,
		},
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
