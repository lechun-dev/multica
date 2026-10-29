package realtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/testutil/wswire"
	"github.com/multica-ai/multica/server/internal/wscompress"
)

func TestRealtimeCompressionAuthBroadcastAndReconnect(t *testing.T) {
	controller, err := wscompress.New(wscompress.Config{Realtime: wscompress.EndpointPolicy{Negotiate: true, Send: true, PreAuth: true, Rule: wscompress.Rule{Default: true}}})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	hub.SetCompressionController(controller)
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleWebSocket(hub, &mockMembershipChecker{}, nil, nil, w, r)
	}))
	defer server.Close()
	for _, support := range []bool{true, false, true} {
		var capture wswire.Capture
		c, response, err := capture.Dialer(support).Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?workspace_id="+testWorkspaceID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") != support {
			t.Fatal("wrong extension")
		}
		auth, _ := json.Marshal(map[string]any{"type": "auth", "payload": map[string]string{"token": makeTestToken(t)}})
		if err := c.WriteMessage(websocket.TextMessage, auth); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, ack, err := c.ReadMessage()
		if err != nil || !bytes.Contains(ack, []byte("auth_ack")) {
			t.Fatalf("auth: %v", err)
		}
		waitFor(t, "registration", func() bool { return totalClients(hub) == 1 })
		for i := range 12 {
			data, _ := json.Marshal(map[string]any{"type": "task:progress", "payload": map[string]any{"task_id": "1", "summary": strings.Repeat("中文输出", 700), "step": i}})
			hub.Broadcast(data)
			_, got, err := c.ReadMessage()
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("stream %d: %v", i, err)
			}
		}
		frames, err := capture.Frames()
		if err != nil {
			t.Fatal(err)
		}
		if frames[0].Compressed {
			t.Fatal("auth ACK compressed")
		}
		count := 0
		for _, frame := range frames {
			if frame.Opcode == websocket.TextMessage {
				if count > 0 && frame.Compressed != support {
					t.Fatal("business frame compression mismatch")
				}
				count++
			}
		}
		if count != 13 {
			t.Fatalf("data frames=%d", count)
		}
		c.Close()
		waitFor(t, "disconnect", func() bool { return totalClients(hub) == 0 })
	}
}

func TestRealtimeCompressedInflatedLimitBeforeAndAfterAuth(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "preauth", true: "authenticated"}[authenticated], func(t *testing.T) {
			controller, _ := wscompress.New(wscompress.Config{Realtime: wscompress.EndpointPolicy{Negotiate: true, PreAuth: true}})
			hub := NewHub()
			hub.SetCompressionController(controller)
			go hub.Run()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				HandleWebSocket(hub, &mockMembershipChecker{}, nil, nil, w, r)
			}))
			defer server.Close()
			c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?workspace_id="+testWorkspaceID, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if authenticated {
				b, _ := json.Marshal(map[string]any{"type": "auth", "payload": map[string]string{"token": makeTestToken(t)}})
				c.WriteMessage(websocket.TextMessage, b)
				c.SetReadDeadline(time.Now().Add(3 * time.Second))
				if _, _, err := c.ReadMessage(); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("z"), int(inboundReadLimit)+1)); err != nil {
				t.Fatal(err)
			}
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err = c.ReadMessage()
			if !websocket.IsCloseError(err, 1009) {
				t.Fatalf("close=%v", err)
			}
		})
	}
}
