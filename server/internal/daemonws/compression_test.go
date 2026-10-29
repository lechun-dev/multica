package daemonws

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/testutil/wswire"
	"github.com/multica-ai/multica/server/internal/wscompress"
)

func TestDaemonEndpointCompressionAndOldClients(t *testing.T) {
	controller, _ := wscompress.New(wscompress.Config{Daemon: wscompress.EndpointPolicy{Negotiate: true, Send: true, Rule: wscompress.Rule{Default: true}}})
	hub := NewHub()
	hub.SetCompressionController(controller)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{UserID: "user", RuntimeIDs: []string{"runtime-1"}})
	}))
	defer server.Close()
	for _, support := range []bool{true, false, true} {
		var capture wswire.Capture
		c, response, err := capture.Dialer(support).Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/daemon/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") != support {
			t.Fatal("wrong extension")
		}
		deadline := time.Now().Add(3 * time.Second)
		for hub.RuntimeConnectionCount("runtime-1") != 1 {
			if time.Now().After(deadline) {
				t.Fatal("registration timed out")
			}
			time.Sleep(time.Millisecond)
		}
		for range 12 {
			hub.NotifyTaskAvailable("runtime-1", strings.Repeat("task-中文", 300))
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, raw, err := c.ReadMessage()
			if err != nil || !bytes.Contains(raw, []byte("daemon:task_available")) {
				t.Fatalf("read: %v", err)
			}
		}
		frames, err := capture.Frames()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range frames {
			if f.Opcode == websocket.TextMessage && f.Compressed != support {
				t.Fatal("actual frame compression mismatch")
			}
		}
		c.Close()
		deadline = time.Now().Add(3 * time.Second)
		for hub.RuntimeConnectionCount("runtime-1") != 0 {
			if time.Now().After(deadline) {
				t.Fatal("disconnect timed out")
			}
			time.Sleep(time.Millisecond)
		}
	}
}
func TestDaemonEndpointCompressedInflatedLimit(t *testing.T) {
	controller, _ := wscompress.New(wscompress.Config{Daemon: wscompress.EndpointPolicy{Negotiate: true, Rule: wscompress.Rule{Default: true}}})
	hub := NewHub()
	hub.SetCompressionController(controller)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWebSocket(w, r, ClientIdentity{UserID: "user", RuntimeIDs: []string{"runtime-1"}})
	}))
	defer server.Close()
	c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/daemon/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("z"), 65537))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = c.ReadMessage()
	if !websocket.IsCloseError(err, 1009) {
		t.Fatalf("close=%v", err)
	}
}
