package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDelivered  = "delivered"
	StatusFailed     = "failed"
	StatusSkipped    = "skipped"
)

// OutboxItem is the durable unit consumed by the worker. A SQL-backed store
// can implement Store without importing any Multica package.
type OutboxItem struct {
	ID             string
	IdempotencyKey string
	Message        Message
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store is intentionally small so the production module can be backed by
// PostgreSQL later while local/mock uses the in-memory implementation below.
type Store interface {
	Enqueue(ctx context.Context, item OutboxItem) (created bool, err error)
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error)
	MarkDelivered(ctx context.Context, id string, at time.Time) error
	MarkRetry(ctx context.Context, id string, next time.Time, attempts int, reason string) error
	MarkFailed(ctx context.Context, id string, at time.Time, attempts int, reason string) error
}

type OutboxReader interface {
	List(ctx context.Context, workspaceID string, limit int) ([]OutboxItem, error)
}

func IdempotencyKey(message Message) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", message.EventID, message.WorkspaceID, message.TargetKind, message.TargetID, message.ChannelID+"\x00"+message.DingUserID)
	return hex.EncodeToString(h.Sum(nil))
}

// EnqueueMessages persists successful routing intents and failed routing
// results independently. Routing failures are returned to the caller for
// audit, while only sendable messages enter the worker queue.
func EnqueueMessages(ctx context.Context, store Store, messages []Message, now time.Time) error {
	for _, message := range messages {
		item := OutboxItem{ID: IdempotencyKey(message), IdempotencyKey: IdempotencyKey(message), Message: message, Status: StatusPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		if _, err := store.Enqueue(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

type MemoryStore struct {
	mu    sync.Mutex
	items map[string]OutboxItem
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{items: make(map[string]OutboxItem)} }

func (s *MemoryStore) Enqueue(_ context.Context, item OutboxItem) (bool, error) {
	if item.IdempotencyKey == "" || item.ID == "" {
		return false, errors.New("outbox item id and idempotency key are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[item.IdempotencyKey]; exists {
		return false, nil
	}
	s.items[item.IdempotencyKey] = item
	return true, nil
}

func (s *MemoryStore) ClaimDue(_ context.Context, now time.Time, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	claimed := make([]OutboxItem, 0, limit)
	for key, item := range s.items {
		if len(claimed) >= limit || item.Status != StatusPending || item.NextAttemptAt.After(now) {
			continue
		}
		item.Status, item.UpdatedAt = StatusProcessing, now
		item.Attempts++
		s.items[key] = item
		claimed = append(claimed, item)
	}
	return claimed, nil
}

func (s *MemoryStore) MarkDelivered(_ context.Context, id string, at time.Time) error {
	return s.update(id, func(item *OutboxItem) { item.Status, item.UpdatedAt, item.LastError = StatusDelivered, at, "" })
}

func (s *MemoryStore) MarkRetry(_ context.Context, id string, next time.Time, attempts int, reason string) error {
	return s.update(id, func(item *OutboxItem) {
		item.Status, item.NextAttemptAt, item.Attempts, item.LastError, item.UpdatedAt = StatusPending, next, attempts, reason, time.Now()
	})
}

func (s *MemoryStore) MarkFailed(_ context.Context, id string, at time.Time, attempts int, reason string) error {
	return s.update(id, func(item *OutboxItem) {
		item.Status, item.Attempts, item.LastError, item.UpdatedAt = StatusFailed, attempts, reason, at
	})
}

func (s *MemoryStore) update(id string, fn func(*OutboxItem)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := id
	item, ok := s.items[key]
	if !ok {
		for candidate, value := range s.items {
			if value.ID == id {
				key, item, ok = candidate, value, true
				break
			}
		}
	}
	if !ok {
		return fmt.Errorf("outbox item %q not found", id)
	}
	fn(&item)
	s.items[key] = item
	return nil
}

func (s *MemoryStore) Snapshot() []OutboxItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]OutboxItem, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return items
}

func (s *MemoryStore) List(_ context.Context, workspaceID string, limit int) ([]OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]OutboxItem, 0)
	for _, item := range s.items {
		if workspaceID != "" && item.Message.WorkspaceID != workspaceID {
			continue
		}
		items = append(items, item)
		if limit > 0 && len(items) >= limit {
			break
		}
	}
	return items, nil
}
