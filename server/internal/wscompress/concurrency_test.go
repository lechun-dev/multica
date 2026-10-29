package wscompress

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConcurrentConnectionsDuringLivePolicyChanges(t *testing.T) {
	for _, endpoint := range []Endpoint{Realtime, Daemon} {
		t.Run(string(endpoint), func(t *testing.T) {
			controller := mustController(t)
			payload := progress(4096)
			if endpoint == Daemon {
				payload = bytes.Replace(payload, []byte(`"task:progress"`), []byte(`"daemon:task_available"`), 1)
				payload = bytes.Replace(payload, []byte(`"summary"`), []byte(`"runtime_id"`), 1)
			}
			results := make(chan error, 16)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, s, err := Upgrade(controller, endpoint, websocket.Upgrader{}, w, r, "u", "w")
				if err != nil {
					results <- err
					return
				}
				defer c.Close()
				c.SetWriteDeadline(time.Now().Add(10 * time.Second))
				c.SetReadDeadline(time.Now().Add(10 * time.Second))
				pong := false
				c.SetPongHandler(func(string) error { pong = true; return nil })
				if err = c.WriteControl(websocket.PingMessage, []byte("probe"), time.Now().Add(time.Second)); err != nil {
					results <- err
					return
				}
				for range 20 {
					if err = s.WriteMessage(websocket.TextMessage, payload); err != nil {
						results <- err
						return
					}
				}
				_, ack, err := ReadMessage(c, 64<<10)
				if err == nil && (!pong || string(ack) != "ack") {
					err = fmt.Errorf("heartbeat or acknowledgement lost")
				}
				results <- err
			}))
			defer server.Close()
			stop, stopped := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(stopped)
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				enabled := false
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						config := enabledConfig()
						config.Realtime.Send, config.Daemon.Send = enabled, enabled
						_ = controller.replace(config)
						enabled = !enabled
					}
				}
			}()
			var clients sync.WaitGroup
			for i := range 16 {
				clients.Go(func() {
					c, _, err := (&websocket.Dialer{EnableCompression: i%2 == 0}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer c.Close()
					c.SetReadDeadline(time.Now().Add(10 * time.Second))
					for range 20 {
						_, got, err := c.ReadMessage()
						if err != nil {
							t.Error(err)
							return
						}
						if !bytes.Equal(got, payload) {
							t.Error("payload changed")
							return
						}
					}
					if err := c.WriteMessage(websocket.TextMessage, []byte("ack")); err != nil {
						t.Error(err)
					}
				})
			}
			clients.Wait()
			close(stop)
			<-stopped
			for range 16 {
				select {
				case err := <-results:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("connection did not finish")
				}
			}
		})
	}
}

type partialConn struct{ net.Conn }

func (partialConn) Write(p []byte) (int, error) { return min(3, len(p)), net.ErrClosed }
func (partialConn) Close() error                { return nil }

func TestObserverPartialFailureAndUncountedClose(t *testing.T) {
	stats := &Counters{}
	o := &observer{Conn: partialConn{}, stats: stats, active: true}
	n, err := o.Write([]byte{0x81, 5, 'a', 'b', 'c', 'd', 'e'})
	if n != 3 || err == nil || stats.FailedWriteBytes.Load() != 3 || stats.DataHeaders.Load() != 2 || stats.DataPayload.Load() != 1 || o.completed() != 0 {
		t.Fatal("failed frame incorrectly counted as a completed message")
	}
	o.Close()
	o.Close()
	if stats.Active.Load() != 0 {
		t.Fatal("failed upgrade decremented active connections")
	}
}
