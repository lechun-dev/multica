package wscompress

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Counters contain sizes and timings only. Labels are fixed endpoint names;
// there are no user IDs, URLs, event names or business data in these metrics.
type Counters struct {
	Connections        atomic.Uint64
	Negotiated         atomic.Uint64
	Active             atomic.Int64
	ActiveNegotiated   atomic.Int64
	Original           atomic.Uint64
	CompressedOriginal atomic.Uint64
	Sent               atomic.Uint64
	Messages           atomic.Uint64
	Compressed         atomic.Uint64
	WriteErrors        atomic.Uint64
	WriteNanos         atomic.Uint64
	WriteAttempts      atomic.Uint64
	WriteBuckets       [10]atomic.Uint64
	// Transport includes partial/failed writes and pre-auth data. Successful
	// business bytes above form a matched pair for weighted savings.
	DataPayload      atomic.Uint64
	DataHeaders      atomic.Uint64
	ControlBytes     atomic.Uint64
	FailedWriteBytes atomic.Uint64
	InboundTooLarge  atomic.Uint64
}

var WriteBounds = [...]float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 10}

var RealtimeStats, DaemonStats Counters
var ReloadErrors atomic.Uint64

func Stats(endpoint Endpoint) *Counters {
	if endpoint == Daemon {
		return &DaemonStats
	}
	return &RealtimeStats
}

// observer parses only frame headers and skips payload bytes without copying
// them. Gorilla serializes network writes, including WriteControl. The mutex
// also protects snapshots and connection close bookkeeping.
type observer struct {
	net.Conn
	mu                              sync.Mutex
	active                          bool
	header                          [14]byte
	have, need                      int
	remaining                       uint64
	control, fin, compressed        bool
	messagePayload, completePayload uint64
	stats                           *Counters
	once                            sync.Once
	counted                         atomic.Bool
	negotiated                      bool
}

func (o *observer) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.Conn.Write(p)
	if o.active {
		o.consume(p[:n])
		if err != nil {
			o.stats.FailedWriteBytes.Add(uint64(n))
		}
	}
	return n, err
}

func (o *observer) Close() error {
	o.once.Do(func() {
		if o.counted.Load() {
			o.stats.Active.Add(-1)
			if o.negotiated {
				o.stats.ActiveNegotiated.Add(-1)
			}
		}
	})
	return o.Conn.Close()
}

func (o *observer) completed() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.completePayload
}

func (o *observer) consume(p []byte) {
	for len(p) > 0 {
		if o.remaining > 0 {
			n := min(uint64(len(p)), o.remaining)
			if o.control {
				o.stats.ControlBytes.Add(n)
			} else {
				o.stats.DataPayload.Add(n)
				o.messagePayload += n
			}
			p = p[int(n):]
			o.remaining -= n
			if o.remaining == 0 {
				o.finishFrame()
			}
			continue
		}
		if o.need == 0 {
			o.need = 2
		}
		n := min(len(p), o.need-o.have)
		copy(o.header[o.have:], p[:n])
		if o.header[0]&8 != 0 {
			o.stats.ControlBytes.Add(uint64(n))
		} else {
			o.stats.DataHeaders.Add(uint64(n))
		}
		o.have += n
		p = p[n:]
		if o.have < o.need {
			continue
		}
		if o.need == 2 {
			switch o.header[1] & 127 {
			case 126:
				o.need += 2
			case 127:
				o.need += 8
			}
			if o.header[1]&128 != 0 {
				o.need += 4
			}
			if o.have < o.need {
				continue
			}
		}
		o.control = o.header[0]&8 != 0
		o.fin = o.header[0]&128 != 0
		if !o.control && o.header[0]&15 != 0 {
			o.compressed = o.header[0]&64 != 0
		}
		o.remaining = uint64(o.header[1] & 127)
		switch o.remaining {
		case 126:
			o.remaining = uint64(binary.BigEndian.Uint16(o.header[2:4]))
		case 127:
			o.remaining = binary.BigEndian.Uint64(o.header[2:10])
		}
		if o.remaining == 0 {
			o.finishFrame()
		}
	}
}

func (o *observer) finishFrame() {
	if !o.control && o.fin {
		o.completePayload += o.messagePayload
		o.messagePayload = 0
		if o.compressed {
			o.stats.Compressed.Add(1)
		}
	}
	o.have, o.need = 0, 0
}

type upgradeWriter struct {
	http.ResponseWriter
	observer *observer
	stats    *Counters
}

func (w *upgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.observer = &observer{Conn: c, stats: w.stats}
	return w.observer, rw, nil
}

// compatibleOffer is deliberately stricter than Gorilla 1.5.3, whose Upgrader
// ignores extension parameters. Never accept a window smaller than its fixed
// 15-bit compressor or an unknown/duplicate parameter. Other offers stay plain.
func compatibleOffer(header http.Header) bool {
	for _, value := range header.Values("Sec-WebSocket-Extensions") {
		for _, offer := range strings.Split(value, ",") {
			parts := strings.Split(offer, ";")
			if strings.TrimSpace(parts[0]) != "permessage-deflate" {
				continue
			}
			seen := map[string]bool{}
			ok := true
			for _, p := range parts[1:] {
				kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
				key := strings.TrimSpace(kv[0])
				if seen[key] {
					ok = false
					break
				}
				seen[key] = true
				switch key {
				case "server_no_context_takeover", "client_no_context_takeover":
					ok = len(kv) == 1
				case "server_max_window_bits", "client_max_window_bits":
					if len(kv) == 1 {
						ok = key == "client_max_window_bits"
					} else {
						value := strings.TrimSpace(kv[1])
						ok = value == "15" || value == `"15"`
					}
				default:
					ok = false
				}
				if !ok {
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

type Session struct {
	conn                *websocket.Conn
	controller          *Controller
	endpoint            Endpoint
	userID, workspaceID string
	transport           *observer
}

func Upgrade(controller *Controller, endpoint Endpoint, upgrader websocket.Upgrader, w http.ResponseWriter, r *http.Request, userID, workspaceID string) (*websocket.Conn, *Session, error) {
	s := controller.policy()
	enabled, level := false, 1
	if s != nil {
		p := s.endpoint(endpoint)
		enabled = p.Negotiate && ((endpoint == Realtime && userID == "" && p.PreAuth) || (userID != "" && s.eligible(endpoint, userID, workspaceID)) || (endpoint == Daemon && s.eligible(endpoint, userID, workspaceID)))
		level = s.config.Level
	}
	enabled = enabled && compatibleOffer(r.Header)
	upgrader.EnableCompression = enabled
	if enabled {
		// Select only the compatible offer. Gorilla otherwise chooses the first
		// offer by name while ignoring its parameters.
		r = r.Clone(r.Context())
		r.Header = r.Header.Clone()
		r.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate; server_no_context_takeover; client_no_context_takeover")
	}
	stats := Stats(endpoint)
	wrapper := &upgradeWriter{ResponseWriter: w, stats: stats}
	conn, err := upgrader.Upgrade(wrapper, r, nil)
	if err != nil {
		return nil, nil, err
	}
	stats.Connections.Add(1)
	stats.Active.Add(1)
	if enabled {
		stats.Negotiated.Add(1)
		stats.ActiveNegotiated.Add(1)
	}
	wrapper.observer.mu.Lock()
	wrapper.observer.active = true
	wrapper.observer.negotiated = enabled
	wrapper.observer.counted.Store(true)
	wrapper.observer.mu.Unlock()
	conn.EnableWriteCompression(false)
	// A policy level edit affects new handshakes; never change it during writes.
	_ = conn.SetCompressionLevel(level) // Validated before publication.
	session := &Session{conn: conn, controller: controller, endpoint: endpoint, userID: userID, workspaceID: workspaceID, transport: wrapper.observer}
	return conn, session, nil
}

// Identity must be set after first-message authentication and before starting
// either connection pump; it is immutable once those goroutines start.
func (s *Session) Identity(userID, workspaceID string) { s.userID, s.workspaceID = userID, workspaceID }

// WriteMessage is called only by the existing single writer (or sequential
// pre-auth writes). Concurrent WriteControl remains supported by Gorilla.
func (s *Session) WriteMessage(kind int, data []byte) error {
	start := time.Now()
	config := s.controller.policy()
	compress := false
	if s.transport.negotiated && config != nil && kind == websocket.TextMessage {
		compress = config.endpoint(s.endpoint).Send && len(data) >= config.config.Threshold && config.eligible(s.endpoint, s.userID, s.workspaceID) && safeMessage(s.endpoint, data)
	}
	s.conn.EnableWriteCompression(compress)
	before := s.transport.completed()
	err := s.conn.WriteMessage(kind, data)
	stats := s.transport.stats
	elapsed := time.Since(start)
	stats.WriteNanos.Add(uint64(elapsed))
	for i, bound := range WriteBounds {
		if elapsed.Seconds() <= bound {
			stats.WriteBuckets[i].Add(1)
		}
	}
	stats.WriteAttempts.Add(1)
	if err != nil {
		stats.WriteErrors.Add(1)
		return err
	}
	if kind == websocket.TextMessage || kind == websocket.BinaryMessage {
		stats.Original.Add(uint64(len(data)))
		if compress {
			stats.CompressedOriginal.Add(uint64(len(data)))
		}
		stats.Sent.Add(s.transport.completed() - before)
		stats.Messages.Add(1)
	}
	return nil
}

// Write preserves lightweight synthetic clients used by hub unit tests. Real
// upgraded connections always have a session, including plain old clients.
func Write(s *Session, conn *websocket.Conn, data []byte) error {
	if s != nil {
		return s.WriteMessage(websocket.TextMessage, data)
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// ReadMessage bounds decompressed bytes as well as Gorilla's wire-size limit.
// It returns the library's existing sentinel so callers keep their 1009 paths.
func ReadMessage(conn *websocket.Conn, limit int64) (int, []byte, error) {
	kind, reader, err := conn.NextReader()
	if err != nil {
		return kind, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return kind, nil, err
	}
	if int64(len(data)) > limit {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big"), time.Now().Add(time.Second))
		return kind, nil, websocket.ErrReadLimit
	}
	return kind, data, nil
}
