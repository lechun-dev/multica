package wscompress

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestBrowserCompressionProbe is an opt-in loopback fixture for an actual
// browser's native WebSocket. It has no credentials, database or real traffic.
func TestBrowserCompressionProbe(t *testing.T) {
	if os.Getenv("MULTICA_WS_BROWSER_PROBE") != "1" {
		t.Skip("opt-in native browser fixture")
	}
	c := mustController(t)
	done := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/done" {
			once.Do(func() { close(done) })
			return
		}
		if r.URL.Path != "/ws" && r.URL.Path != "/api/daemon/ws" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<!doctype html><title>Local WebSocket compression probe</title><p>Synthetic loopback fixture</p>")
			return
		}
		endpoint := Realtime
		if r.URL.Path == "/api/daemon/ws" {
			endpoint = Daemon
		}
		socket, session, err := Upgrade(c, endpoint, websocket.Upgrader{}, w, r, "synthetic-user", "synthetic-workspace")
		if err != nil {
			return
		}
		defer socket.Close()
		data := progress(4096)
		if endpoint == Daemon {
			data = []byte(`{"type":"daemon:task_available","payload":{"task_id":"synthetic","runtime_id":"` + strings.Repeat("中文", 1000) + `"}}`)
		}
		for range 10 {
			if err := session.WriteMessage(websocket.TextMessage, data); err != nil {
				t.Error(err)
				return
			}
		}
		socket.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, _, _ = ReadMessage(socket, 65536)
	}))
	defer server.Close()
	fmt.Printf("BROWSER_PROBE_URL=%s\n", server.URL)
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("browser probe timed out")
	}
	for _, endpoint := range []Endpoint{Realtime, Daemon} {
		s := Stats(endpoint)
		fmt.Printf("BROWSER_PROBE endpoint=%s negotiated=%d compressed=%d original=%d sent=%d\n", endpoint, s.Negotiated.Load(), s.Compressed.Load(), s.Original.Load(), s.Sent.Load())
		if s.Compressed.Load() < 10 {
			t.Errorf("%s: browser did not exercise actual compressed messages", endpoint)
		}
	}
}
