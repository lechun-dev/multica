package wscompress

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageSafetyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint Endpoint
		data     string
		want     bool
	}{
		{"text", Realtime, `{"type":"task:message","payload":{"task_id":"1","type":"text","content":"中文文本"}}`, true},
		{"tool", Realtime, `{"type":"task:message","payload":{"type":"tool","input":{"auth_token":"synthetic"}}}`, false},
		{"rpc", Daemon, `{"type":"daemon:rpc_response","payload":{"body":{"tasks":[]}}}`, false},
		{"unknown field", Realtime, `{"type":"task:progress","payload":{"task_id":"1","summary":"ok","credentials":"opaque"}}`, false},
		{"unknown event", Realtime, `{"type":"new:event","payload":{}}`, false},
		{"escaped key", Realtime, `{"type":"task:progress","payload":{"task_id":"1","summary":"ok","auth_\u0074oken":"opaque"}}`, false},
		{"signed URL", Realtime, `{"type":"task:progress","payload":{"summary":"https://synthetic.test/file?signature=opaque"}}`, false},
		{"media", Realtime, `{"type":"chat:message","payload":{"content":"data:image/png;base64,abc"}}`, false},
		{"heartbeat", Daemon, `{"type":"daemon:heartbeat_ack","payload":{"runtime_id":"1","status":"ok"}}`, true},
		{"nested unknown", Daemon, `{"type":"daemon:heartbeat_ack","payload":{"runtime_id":"1","pending_update":{"id":"1","target_version":"1","auth_token":"opaque"}}}`, false},
		{"auth", Realtime, `{"type":"auth_ack"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeMessage(tc.endpoint, []byte(tc.data)); got != tc.want {
				t.Fatalf("allowed=%v want=%v", got, tc.want)
			}
		})
	}
	for _, secret := range []string{"Bearer synthetic", "auth_token=synthetic", "api_key=synthetic", "PASSWORD synthetic", "https://example.test", "data:application/zip;base64,AA", "-----BEGIN PRIVATE KEY-----"} {
		b, _ := json.Marshal(map[string]any{"type": "task:progress", "payload": map[string]any{"summary": strings.Repeat("x", 4096) + secret}})
		if safeMessage(Realtime, b) {
			t.Fatalf("marker after 4096 bytes accepted: %s", secret)
		}
	}
}
func TestClientMessageSafetyPolicy(t *testing.T) {
	for message, want := range map[string]bool{
		`{"type":"daemon:heartbeat","payload":{"runtime_id":"1"}}`:                                                                                      true,
		`{"type":"daemon:rpc_request","payload":{"method":"tasks.claim","request_id":"1","body":{"daemon_id":"1","runtime_ids":["1"],"max_tasks":32}}}`: true,
		`{"type":"daemon:rpc_request","payload":{"method":"other","body":{}}}`:                                                                          false,
		`{"type":"daemon:heartbeat","payload":{"runtime_id":"1","auth_token":"synthetic"}}`:                                                             false,
	} {
		if got := ClientMessageAllowed([]byte(message)); got != want {
			t.Fatalf("allowed=%v want=%v for %s", got, want, message)
		}
	}
}
