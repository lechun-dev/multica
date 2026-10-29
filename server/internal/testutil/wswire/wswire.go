// Package wswire inspects actual WebSocket frames in tests only. Captured
// payloads are synthetic fixtures and must never be used for production logs.
package wswire

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"github.com/gorilla/websocket"
)

type Capture struct {
	mu   sync.Mutex
	data []byte
}
type conn struct {
	net.Conn
	capture *Capture
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.capture.mu.Lock()
	c.capture.data = append(c.capture.data, p[:n]...)
	c.capture.mu.Unlock()
	return n, err
}
func (c *Capture) Dialer(compression bool) *websocket.Dialer {
	return &websocket.Dialer{EnableCompression: compression, NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		socket, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &conn{socket, c}, nil
	}}
}

type Frame struct {
	Final, Compressed bool
	Opcode            byte
	Payload           []byte
}

func (c *Capture) Frames() ([]Frame, error) {
	c.mu.Lock()
	data := bytes.Clone(c.data)
	c.mu.Unlock()
	start := bytes.Index(data, []byte("\r\n\r\n"))
	if start < 0 {
		return nil, fmt.Errorf("handshake incomplete")
	}
	data = data[start+4:]
	var frames []Frame
	for len(data) >= 2 {
		n := uint64(data[1] & 127)
		header := 2
		if n == 126 {
			if len(data) < 4 {
				break
			}
			n = uint64(binary.BigEndian.Uint16(data[2:4]))
			header = 4
		}
		if n == 127 {
			if len(data) < 10 {
				break
			}
			n = binary.BigEndian.Uint64(data[2:10])
			header = 10
		}
		masked := data[1]&128 != 0
		if masked {
			header += 4
		}
		if uint64(len(data)) < uint64(header)+n {
			break
		}
		payload := bytes.Clone(data[header : header+int(n)])
		if masked {
			for i := range payload {
				payload[i] ^= data[header-4+i%4]
			}
		}
		frames = append(frames, Frame{data[0]&128 != 0, data[0]&64 != 0, data[0] & 15, payload})
		data = data[header+int(n):]
	}
	return frames, nil
}

type listener struct {
	net.Listener
	capture *Capture
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &conn{c, l.capture}, nil
}

// Listener captures client-to-server frames, including their masking keys.
func (c *Capture) Listener(l net.Listener) net.Listener { return &listener{l, c} }
