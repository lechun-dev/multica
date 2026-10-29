//go:build darwin || linux

package wscompress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// BenchmarkTransport uses deterministic synthetic traffic, never customer data.
// It includes small messages and intentionally skipped sensitive RPC responses.
// The fixture mix is not a forecast of production savings.
func BenchmarkTransport(b *testing.B) {
	for _, endpoint := range []Endpoint{Realtime, Daemon} {
		for _, enabled := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/compression=%v", endpoint, enabled), func(b *testing.B) {
				config := Config{}
				if enabled {
					config = enabledConfig()
				}
				controller, _ := New(config)
				var fixtures [][]byte
				if endpoint == Realtime {
					for _, size := range []int{30, 90, 500, 1024, 4096, 16384} {
						data, _ := json.Marshal(map[string]any{"type": "task:progress", "payload": map[string]any{"task_id": "task-1", "summary": strings.Repeat("中文 streaming ", size/17+1)}})
						fixtures = append(fixtures, data)
					}
				} else {
					// Use bounded IDs and real batch fields, rather than inflating
					// notification IDs with prose to cross the byte threshold.
					for _, count := range []int{0, 1, 4, 12, 48, 192} {
						imports := make([]map[string]string, count)
						for i := range imports {
							imports[i] = map[string]string{"id": fmt.Sprintf("%08x-1234-4567-8901-%012x", i*7919, i*104729), "skill_key": fmt.Sprintf("synthetic-skill-%08x", i*7919)}
						}
						data, _ := json.Marshal(map[string]any{"type": "daemon:heartbeat_ack", "payload": map[string]any{"runtime_id": "12345678-1234-4567-8901-123456789012", "status": "ok", "pending_local_skill_imports": imports}})
						fixtures = append(fixtures, data)
					}
					fixtures = append(fixtures, []byte(`{"type":"daemon:task_available","payload":{"runtime_id":"12345678-1234-4567-8901-123456789012","task_id":"87654321-1234-4567-8901-123456789012"}}`))
				}
				fixtures = append(fixtures, []byte(`{"type":"daemon:rpc_response","payload":{"body":{"auth_token":"synthetic","instructions":"`+strings.Repeat("x", 16384)+`"}}}`))
				// A list with less repetition than prose exercises the list allowlist.
				if endpoint == Realtime {
					ids := make([]string, 200)
					for i := range ids {
						ids[i] = fmt.Sprintf("%08x-%08x", i*7919, i*104729)
					}
					data, _ := json.Marshal(map[string]any{"type": "inbox:batch-read", "payload": map[string]any{"ids": ids, "count": 200}})
					fixtures = append(fixtures, data)
				}
				writes := make(chan []byte)
				ready := make(chan *Session, 1)
				errs := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, s, err := Upgrade(controller, endpoint, websocket.Upgrader{}, w, r, "user", "workspace")
					if err != nil {
						errs <- err
						return
					}
					defer c.Close()
					ready <- s
					for data := range writes {
						if err := s.WriteMessage(websocket.TextMessage, data); err != nil {
							errs <- err
							return
						}
					}
					errs <- nil
				}))
				defer server.Close()
				c, _, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if err != nil {
					b.Fatal(err)
				}
				defer c.Close()
				s := <-ready
				stats := s.transport.stats
				original, sent, compressed := stats.Original.Load(), stats.Sent.Load(), stats.Compressed.Load()
				samples := make([]int64, 0, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				cpuStart := processCPUTime()
				for i := range b.N {
					start := time.Now()
					want := fixtures[i%len(fixtures)]
					writes <- want
					_, got, err := c.ReadMessage()
					if err != nil {
						b.Fatal(err)
					}
					if !bytes.Equal(want, got) {
						b.Fatal("payload changed")
					}
					samples = append(samples, time.Since(start).Nanoseconds())
				}
				cpuElapsed := processCPUTime() - cpuStart
				b.StopTimer()
				close(writes)
				if err := <-errs; err != nil {
					b.Fatal(err)
				}
				raw, wire := stats.Original.Load()-original, stats.Sent.Load()-sent
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				b.ReportMetric(float64(cpuElapsed.Nanoseconds())/float64(b.N), "process-cpu-ns/op")
				b.ReportMetric(float64(raw), "original-B-total")
				b.ReportMetric(float64(wire), "wire-payload-B-total")
				b.ReportMetric(float64(raw)/float64(b.N), "original-B/msg")
				b.ReportMetric(float64(wire)/float64(b.N), "wire-payload-B/msg")
				b.ReportMetric(100*(1-float64(wire)/float64(raw)), "saved-%")
				b.ReportMetric(float64(samples[(len(samples)-1)*95/100])/1e3, "p95-us")
				b.ReportMetric(float64(stats.Compressed.Load()-compressed), "compressed-msgs")
			})
		}
	}
}

// Process CPU covers both local peers, schema checks, compression and GC.
// It excludes setup/compilation and is not production server-only CPU usage.
func processCPUTime() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		panic(err)
	}
	return time.Duration(usage.Utime.Sec+usage.Stime.Sec)*time.Second + time.Duration(usage.Utime.Usec+usage.Stime.Usec)*time.Microsecond
}
