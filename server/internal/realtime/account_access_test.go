package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/auth"
)

// 2026-10-19 coder(lq): Exercise a real already-connected socket, not just
// login-time validation. Revocation must precede the next business payload.
func TestConnectedAccountRevocation(t *testing.T) {
	for _, scenario := range []string{"revoked", "database unavailable", "idle socket"} {
		t.Run(scenario, func(t *testing.T) {
			unavailable := scenario == "database unavailable"
			var denied atomic.Bool
			hub := NewHub()
			go hub.Run()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				client := &Client{hub: hub, conn: conn, send: make(chan []byte, 8), userID: "test-user", workspaceID: "test-workspace",
					accountCheck: func(context.Context) error {
						if denied.Load() {
							if unavailable {
								return errors.New("db offline")
							}
							return auth.ErrTemporarilyDisabledUser
						}
						return nil
					},
				}
				hub.register <- client
				go client.writePump()
				go client.readPump()
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			waitFor(t, "registered", func() bool { return totalClients(hub) == 1 })
			denied.Store(true)
			if scenario != "idle socket" {
				hub.SendToUser("test-user", []byte(`{"type":"issue:updated","payload":{"secret":"never deliver"}}`))
			}
			conn.SetReadDeadline(time.Now().Add(accountAccessCheckInterval + 2*time.Second))
			_, raw, err := conn.ReadMessage()
			if unavailable {
				if err == nil {
					t.Fatalf("database failure delivered %s", raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"type":"account:disabled"`) || strings.Contains(string(raw), "secret") {
				t.Fatalf("unexpected frame %s", raw)
			}
			if _, _, err := conn.ReadMessage(); err == nil {
				t.Fatal("revoked socket remained open")
			}
		})
	}
}
