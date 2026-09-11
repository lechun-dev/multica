package dingtalkpersonal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
