package agent

import "sync"

// 2026-10-08 coder(lq): Serialize stream closure with late ACP notifications after bounded reader drain.
type acpMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

func newACPMessageStream(size int) *acpMessageStream {
	return &acpMessageStream{ch: make(chan Message, size)}
}

func (s *acpMessageStream) send(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, msg)
}

func (s *acpMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}
