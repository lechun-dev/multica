package wscompress

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/testutil/wswire"
)

func enabledConfig() Config {
	p := EndpointPolicy{Negotiate: true, Send: true, PreAuth: true, Rule: Rule{Default: true}}
	return Config{Realtime: p, Daemon: p}
}
func progress(size int) []byte {
	b, _ := json.Marshal(map[string]any{"type": "task:progress", "payload": map[string]any{"task_id": "task-1", "summary": ""}})
	b, _ = json.Marshal(map[string]any{"type": "task:progress", "payload": map[string]any{"task_id": "task-1", "summary": strings.Repeat("x", size-len(b))}})
	return b
}
func TestWireCompressionAndLiveRollback(t *testing.T) {
	for _, endpoint := range []Endpoint{Realtime, Daemon} {
		t.Run(string(endpoint), func(t *testing.T) {
			for _, support := range []bool{true, false} {
				t.Run(map[bool]string{true: "modern", false: "old"}[support], func(t *testing.T) {
					controller, _ := New(enabledConfig())
					stats := Stats(endpoint)
					beforeOriginal, beforeSent, beforeCompressed := stats.Original.Load(), stats.Sent.Load(), stats.CompressedOriginal.Load()
					writes := make(chan []byte)
					done := make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						c, s, err := Upgrade(controller, endpoint, websocket.Upgrader{}, w, r, "user", "workspace")
						if err != nil {
							return
						}
						defer c.Close()
						defer close(done)
						for data := range writes {
							if s.WriteMessage(websocket.TextMessage, data) != nil {
								return
							}
						}
					}))
					defer server.Close()
					var capture wswire.Capture
					c, response, err := capture.Dialer(support).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					if got := strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate"); got != support {
						t.Fatalf("negotiation=%v", got)
					}
					extension := response.Header.Get("Sec-WebSocket-Extensions")
					if support && (!strings.Contains(extension, "server_no_context_takeover") || !strings.Contains(extension, "client_no_context_takeover")) {
						t.Fatal("context takeover negotiated")
					}
					fixtures := [][]byte{progress(1023), progress(1024), progress(1025), []byte(`{"type":"task:progress","payload":{"task_id":"task-1","summary":"` + strings.Repeat("中文连续输出", 3000) + `"}}`), []byte(`{"type":"daemon:rpc_response","payload":{"body":{"auth_token":"synthetic","text":"` + strings.Repeat("x", 2048) + `"}}}`), progress(2048)}
					if endpoint == Daemon {
						for i := range fixtures {
							if i != 4 {
								fixtures[i] = bytes.Replace(fixtures[i], []byte(`"task:progress"`), []byte(`"daemon:task_available"`), 1)
								fixtures[i] = bytes.Replace(fixtures[i], []byte(`"summary"`), []byte(`"runtime_id"`), 1)
							}
						}
					}
					for i, data := range fixtures {
						if i == 5 {
							if err := controller.replace(Config{}); err != nil {
								t.Fatal(err)
							}
						}
						writes <- data
						c.SetReadDeadline(time.Now().Add(3 * time.Second))
						_, decoded, err := c.ReadMessage()
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(decoded, data) {
							t.Fatal("decoded payload changed")
						}
					}
					frames, err := capture.Frames()
					if err != nil {
						t.Fatal(err)
					}
					var messages []wswire.Frame
					for _, f := range frames {
						if f.Opcode == websocket.TextMessage {
							messages = append(messages, f)
						}
					}
					if len(messages) != len(fixtures) {
						t.Fatalf("messages=%d", len(messages))
					}
					// Large frames can fragment; inspect the first frame's RSV1, then the
					// normal client above proves the full stream inflates to the original.
					for i, f := range messages {
						// Replacing event names adds eight bytes on daemon fixtures.
						want := support && i >= 1 && i <= 3
						if endpoint == Daemon && i == 0 {
							want = support
						}
						if f.Compressed != want {
							t.Fatalf("message %d compressed=%v want=%v", i, f.Compressed, want)
						}
					}
					if support {
						f := messages[1]
						if len(f.Payload) >= len(fixtures[1]) {
							t.Fatal("actual compressed frame did not shrink")
						}
						inflater := flate.NewReader(io.MultiReader(bytes.NewReader(f.Payload), bytes.NewReader([]byte{0, 0, 255, 255, 1, 0, 0, 255, 255})))
						inflated, err := io.ReadAll(inflater)
						inflater.Close()
						if err != nil || !bytes.Equal(inflated, fixtures[1]) {
							t.Fatalf("raw frame inflate: %v", err)
						}
					}
					close(writes)
					<-done
					var original, sent, compressedOriginal uint64
					for _, data := range fixtures {
						original += uint64(len(data))
					}
					for _, frame := range frames {
						if frame.Opcode == websocket.TextMessage || frame.Opcode == 0 {
							sent += uint64(len(frame.Payload))
						}
					}
					if support {
						for i := 1; i <= 3; i++ {
							compressedOriginal += uint64(len(fixtures[i]))
						}
						if endpoint == Daemon {
							compressedOriginal += uint64(len(fixtures[0]))
						}
					}
					if stats.Original.Load()-beforeOriginal != original || stats.Sent.Load()-beforeSent != sent || stats.CompressedOriginal.Load()-beforeCompressed != compressedOriginal {
						t.Fatal("paired byte metrics differ from actual captured frames")
					}
				})
			}
		})
	}
}

func TestInflatedReadLimit(t *testing.T) {
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, err := Upgrade(mustController(t), Realtime, websocket.Upgrader{}, w, r, "u", "w")
		if err != nil {
			result <- err
			return
		}
		defer c.Close()
		c.SetReadLimit(64 << 10)
		_, _, err = ReadMessage(c, 64<<10)
		result <- err
	}))
	defer server.Close()
	c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetCompressionLevel(1)
	if err := c.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("中"), 30000)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = c.ReadMessage()
	if !websocket.IsCloseError(err, 1009) {
		t.Fatalf("close=%v", err)
	}
	if err := <-result; !errors.Is(err, websocket.ErrReadLimit) {
		t.Fatalf("limit error=%v", err)
	}
}
func mustController(t *testing.T) *Controller {
	t.Helper()
	c, err := New(enabledConfig())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPolicyReloadLastGoodAndConcurrency(t *testing.T) {
	c := mustController(t)
	path := filepath.Join(t.TempDir(), "policy.yaml")
	for _, bad := range []string{"unknown: true", "level: 9", "threshold: -1", "realtime:\n  rule:\n    percent:\n      percent: 101", "---\nlevel: 1\n---\nlevel: 2", "level: [synthetic-secret]"} {
		os.WriteFile(path, []byte(bad), 0600)
		err := c.Reload(path)
		if err == nil {
			t.Fatalf("accepted invalid policy %q", bad)
		}
		if strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("parser leaked policy content")
		}
		if !c.policy().config.Realtime.Send {
			t.Fatal("lost last good policy")
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				c.policy().eligible(Realtime, "u", "w")
			}
		})
	}
	for range 20 {
		os.WriteFile(path, []byte("level: 1\nthreshold: 1024\n"), 0600)
		if err := c.Reload(path); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if c.policy().config.Realtime.Send {
		t.Fatal("rollback not applied")
	}
}

func TestCompatibleOffers(t *testing.T) {
	for offer, want := range map[string]bool{"permessage-deflate": true, "permessage-deflate; client_max_window_bits": true, "permessage-deflate; server_max_window_bits=15": true, "permessage-deflate; server_max_window_bits=8": false, "permessage-deflate; server_max_window_bits=+15": false, `permessage-deflate; server_max_window_bits="15"`: true, `permessage-deflate; server_max_window_bits="15`: false, "permessage-deflate; unknown=15": false, "permessage-deflate; client_no_context_takeover=1": false, "permessage-deflate; client_max_window_bits=15; client_max_window_bits=15": false, "permessage-deflate; server_max_window_bits=8, permessage-deflate": true} {
		h := http.Header{}
		h.Set("Sec-WebSocket-Extensions", offer)
		if got := compatibleOffer(h); got != want {
			t.Errorf("%s = %v", offer, got)
		}
	}
}

func TestObserverSplitHeadersFragmentationControlAndSizes(t *testing.T) {
	stats := &Counters{}
	o := &observer{stats: stats}
	frame := func(first byte, n int) []byte {
		h := []byte{first, byte(n)}
		if n >= 65536 {
			h = []byte{first, 127, 0, 0, 0, 0, 0, 0, 0, 0}
			binary.BigEndian.PutUint64(h[2:], uint64(n))
		} else if n >= 126 {
			h = []byte{first, 126, byte(n >> 8), byte(n)}
		}
		return append(h, make([]byte, n)...)
	}
	stream := append(frame(0x41, 130), frame(0x89, 3)...)
	stream = append(stream, frame(0x80, 70000)...)
	stream = append(stream, frame(0x81, 0)...)
	for _, b := range stream {
		o.consume([]byte{b})
	}
	if o.completed() != 70130 || stats.Compressed.Load() != 1 || stats.ControlBytes.Load() != 5 || stats.DataHeaders.Load() != 16 {
		t.Fatalf("payload=%d compressed=%d control=%d headers=%d", o.completed(), stats.Compressed.Load(), stats.ControlBytes.Load(), stats.DataHeaders.Load())
	}
}
