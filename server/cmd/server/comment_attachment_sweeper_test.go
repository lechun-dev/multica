package main

import (
	"context"
	"testing"
	"time"
)

type commentAttachmentCleanerStub struct {
	calls chan struct{}
}

func (s *commentAttachmentCleanerStub) CleanupCommentAttachmentDrafts(context.Context, int32) (int, error) {
	select {
	case s.calls <- struct{}{}:
	default:
	}
	return 0, nil
}

func TestCommentAttachmentSweeperStopsWithContext(t *testing.T) {
	if commentAttachmentSweepInterval != time.Hour {
		t.Fatalf("comment attachment sweep interval = %s, want 1h", commentAttachmentSweepInterval)
	}
	if commentAttachmentSweepBudget <= 0 || commentAttachmentBatchSize <= 0 {
		t.Fatal("comment attachment sweeper must have positive budget and batch size")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cleaner := &commentAttachmentCleanerStub{calls: make(chan struct{}, 1)}
	stopped := make(chan struct{})
	go func() {
		runCommentAttachmentSweeper(ctx, cleaner)
		close(stopped)
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("comment attachment sweeper did not stop with its context")
	}
}
