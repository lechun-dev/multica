package notify

import (
	"context"
	"errors"
	"testing"
	"time"
)

type retryProvider struct {
	calls int
	err   error
}

func (p *retryProvider) Send(context.Context, Message) error { p.calls++; return p.err }

func TestEnqueueMessagesIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	m := Message{EventID: "e1", WorkspaceID: "w1", TargetID: "m1", TargetKind: "member", DingUserID: "d1"}
	if err := EnqueueMessages(context.Background(), store, []Message{m, m}, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot()); got != 1 {
		t.Fatalf("expected one row, got %d", got)
	}
}

func TestWorkerRetriesTransientAndEventuallyDelivers(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(100, 0)
	m := Message{EventID: "e1", WorkspaceID: "w1", TargetID: "m1", TargetKind: "member", DingUserID: "d1"}
	if err := EnqueueMessages(context.Background(), store, []Message{m}, now); err != nil {
		t.Fatal(err)
	}
	p := &retryProvider{err: RetryableError{Err: errors.New("429")}}
	w := Worker{Store: store, Provider: p, Policy: RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second}, Now: func() time.Time { return now }}
	if _, err := w.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	item := store.Snapshot()[0]
	if item.Status != StatusPending || item.Attempts != 1 || item.LastError == "" {
		t.Fatalf("unexpected retry row: %+v", item)
	}
	// Move the clock to the due time and make the second attempt permanent.
	p.err = nil
	w.Now = func() time.Time { return item.NextAttemptAt }
	if _, err := w.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot()[0].Status; got != StatusDelivered {
		t.Fatalf("status=%s", got)
	}
}

func TestWorkerMarksPermanentFailure(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(100, 0)
	m := Message{EventID: "e1", WorkspaceID: "w1", TargetID: "m1", TargetKind: "member", DingUserID: "d1"}
	_ = EnqueueMessages(context.Background(), store, []Message{m}, now)
	w := Worker{Store: store, Provider: &retryProvider{err: errors.New("bad request")}, Now: func() time.Time { return now }}
	if _, err := w.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot()[0].Status; got != StatusFailed {
		t.Fatalf("status=%s", got)
	}
}
