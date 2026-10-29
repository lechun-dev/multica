package daemon

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/testutil/wswire"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWakeupCompressionNegotiationCompatibility(t *testing.T) {
	for _, mode := range []string{"modern", "old", "takeover", "unauthorized", "bad-handshake"} {
		t.Run(mode, func(t *testing.T) {
			var attempts atomic.Int32
			requests := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				requests <- r.Header.Get("Sec-WebSocket-Extensions")
				if mode == "unauthorized" {
					w.WriteHeader(401)
					return
				}
				if mode == "bad-handshake" {
					w.WriteHeader(400)
					return
				}
				if mode == "takeover" && r.Header.Get("Sec-WebSocket-Extensions") != "" {
					c, rw, err := http.NewResponseController(w).Hijack()
					if err != nil {
						return
					}
					defer c.Close()
					hash := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
					fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:]))
					rw.Flush()
					return
				}
				c, err := (&websocket.Upgrader{EnableCompression: mode == "modern"}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer c.Close()
				c.WriteMessage(websocket.TextMessage, []byte("ok"))
			}))
			defer server.Close()
			c, err := dialWakeup(context.Background(), websocket.Dialer{EnableCompression: true}, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if mode == "unauthorized" || mode == "bad-handshake" {
				if err == nil {
					c.Close()
					t.Fatal("expected error")
				}
				if attempts.Load() != 1 {
					t.Fatal("retried authentication/HTTP error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			want := int32(1)
			if mode == "takeover" {
				want = 2
			}
			if attempts.Load() != want {
				t.Fatalf("attempts=%d", attempts.Load())
			}
			if !strings.Contains(<-requests, "permessage-deflate") {
				t.Fatal("compression not requested")
			}
			if mode == "takeover" && (<-requests) != "" {
				t.Fatal("fallback still requested extension")
			}
		})
	}
}

func TestDaemonSingleWriterCompressionPolicy(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			var capture wswire.Capture
			received := make(chan error, 1)
			fixtures := []string{
				`{"type":"daemon:heartbeat","payload":{"runtime_id":"small"}}`,
				`{"type":"daemon:heartbeat","payload":{"runtime_id":"` + strings.Repeat("runtime", 300) + `"}}`,
				`{"type":"daemon:rpc_request","payload":{"request_id":"1","method":"tasks.claim","body":{"daemon_id":"1","runtime_ids":["` + strings.Repeat("runtime", 300) + `"],"max_tasks":32}}}`,
				`{"type":"daemon:rpc_request","payload":{"method":"opaque","body":{"auth_token":"` + strings.Repeat("synthetic", 300) + `"}}}`,
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := (&websocket.Upgrader{EnableCompression: true}).Upgrade(w, r, nil)
				if err != nil {
					received <- err
					return
				}
				defer c.Close()
				for _, want := range fixtures {
					_, got, err := c.ReadMessage()
					if err != nil {
						received <- err
						return
					}
					if string(got) != want {
						received <- fmt.Errorf("payload changed")
						return
					}
				}
				received <- nil
			}))
			server.Listener = capture.Listener(server.Listener)
			server.Start()
			defer server.Close()
			c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.EnableWriteCompression(false)
			c.SetCompressionLevel(1)
			d := &Daemon{cfg: Config{DisableWSCompression: disabled}, logger: slog.Default()}
			writes := make(chan *wsOutbound, len(fixtures))
			done := make(chan struct{})
			go d.runWSWriter(c, writes, done)
			for _, f := range fixtures {
				writes <- &wsOutbound{data: []byte(f)}
			}
			close(writes)
			<-done
			if err := <-received; err != nil {
				t.Fatal(err)
			}
			frames, err := capture.Frames()
			if err != nil {
				t.Fatal(err)
			}
			i := 0
			for _, f := range frames {
				if f.Opcode != websocket.TextMessage {
					continue
				}
				want := !disabled && (i == 1 || i == 2)
				if f.Compressed != want {
					t.Fatalf("frame %d compression=%v want=%v", i, f.Compressed, want)
				}
				i++
			}
			if i != 4 {
				t.Fatalf("messages=%d", i)
			}
		})
	}
}

func TestDaemonRejectsInflatedResponseAbove64MiB(t *testing.T) {
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{EnableCompression: true}).Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer c.Close()
		c.SetWriteDeadline(time.Now().Add(20 * time.Second))
		c.SetReadDeadline(time.Now().Add(20 * time.Second))
		_ = c.SetCompressionLevel(1)
		writer, err := c.NextWriter(websocket.TextMessage)
		if err != nil {
			result <- err
			return
		}
		// Generate a bounded-size stream so the fixture itself need not allocate
		// another complete 64 MiB business message.
		block := []byte(strings.Repeat("x", 64<<10))
		for range 1024 {
			if _, err = writer.Write(block); err != nil {
				result <- err
				return
			}
		}
		if _, err = writer.Write([]byte("x")); err != nil {
			result <- err
			return
		}
		if err = writer.Close(); err != nil {
			result <- err
			return
		}
		_, _, err = c.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
			result <- fmt.Errorf("close=%v", err)
			return
		}
		result <- nil
	}))
	defer server.Close()
	c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	d := New(Config{}, slog.Default())
	err = d.readTaskWakeupMessages(c, make(chan taskWakeup, 1))
	if !errors.Is(err, websocket.ErrReadLimit) {
		t.Fatalf("limit=%v", err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

func TestDaemonReadsCompressed32TaskClaimBatch(t *testing.T) {
	requestIDs := make(chan string, 1)
	instructions := strings.Repeat("synthetic context ", 8192)
	tasks := make([]map[string]any, 32)
	for i := range tasks {
		tasks[i] = map[string]any{"id": fmt.Sprintf("task-%d", i), "agent": map[string]any{"id": "agent-1", "instructions": instructions}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{EnableCompression: true}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		select {
		case id := <-requestIDs:
			frame := mustProtocolFrame(t, protocol.Message{Type: protocol.EventDaemonRPCResponse, Payload: marshalRaw(protocol.RPCResponsePayload{RequestID: id, Status: 200, Body: marshalRaw(map[string]any{"tasks": tasks})})})
			if err := c.WriteMessage(websocket.TextMessage, frame); err != nil {
				t.Error(err)
			}
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			t.Error("missing RPC request")
		}
	}))
	defer server.Close()
	c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	d := New(Config{}, slog.Default())
	d.wsRPC = newWSRPCClient(time.Second)
	d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
		var msg protocol.Message
		if err := json.Unmarshal(frame, &msg); err != nil {
			return nil, err
		}
		var request protocol.RPCRequestPayload
		if err := json.Unmarshal(msg.Payload, &request); err != nil {
			return nil, err
		}
		requestIDs <- request.RequestID
		item := &wsOutbound{data: frame}
		item.beginWrite()
		return item, nil
	})
	readerDone := make(chan error, 1)
	go func() { readerDone <- d.readTaskWakeupMessages(c, make(chan taskWakeup, 1)) }()
	var response struct {
		Tasks []*Task `json:"tasks"`
	}
	status, err := d.wsRPC.Call(context.Background(), "tasks.claim", 10*time.Second, nil, &response)
	if err != nil || status != 200 {
		t.Fatalf("claim status=%d err=%v", status, err)
	}
	if len(response.Tasks) != 32 {
		t.Fatalf("tasks=%d", len(response.Tasks))
	}
	for i, task := range response.Tasks {
		if task.ID != fmt.Sprintf("task-%d", i) || task.Agent == nil || task.Agent.Instructions != instructions {
			t.Fatalf("task %d changed", i)
		}
	}
	c.Close()
	select {
	case <-readerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not exit")
	}
}
