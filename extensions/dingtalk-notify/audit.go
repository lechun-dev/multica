package notify

import (
	"context"
	"sync"
	"time"
)

type DeliveryAudit struct {
	OutboxID    string
	EventID     string
	WorkspaceID string
	TargetID    string
	TargetKind  string
	ChannelType string
	Status      string
	Attempts    int
	Error       string
	Duration    time.Duration
	At          time.Time
}

// AuditSink is optional: Store remains the minimal durable contract while a
// host can attach a database/metrics-backed audit implementation.
type AuditSink interface {
	Record(ctx context.Context, audit DeliveryAudit) error
}

type MemoryAuditSink struct {
	mu      sync.Mutex
	records []DeliveryAudit
}

func NewMemoryAuditSink() *MemoryAuditSink { return &MemoryAuditSink{} }

func (s *MemoryAuditSink) Record(_ context.Context, audit DeliveryAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, audit)
	return nil
}

func (s *MemoryAuditSink) Snapshot() []DeliveryAudit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DeliveryAudit(nil), s.records...)
}
