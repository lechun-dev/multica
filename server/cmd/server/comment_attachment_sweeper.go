package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

const (
	commentAttachmentSweepInterval = time.Hour
	commentAttachmentSweepBudget   = 45 * time.Second
	commentAttachmentBatchSize     = 50
)

type commentAttachmentCleaner interface {
	CleanupCommentAttachmentDrafts(ctx context.Context, limit int32) (int, error)
}

var _ commentAttachmentCleaner = (*service.TaskService)(nil)

// 2026-09-03 coder(lq): Keep abandoned composer uploads out of object storage
// without sharing the source-context sweeper's lifecycle or budget.
func runCommentAttachmentSweeper(ctx context.Context, cleaner commentAttachmentCleaner) {
	ticker := time.NewTicker(commentAttachmentSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, commentAttachmentSweepBudget)
			cleaned, err := cleaner.CleanupCommentAttachmentDrafts(sweepCtx, commentAttachmentBatchSize)
			cancel()
			if err != nil {
				slog.Warn("comment attachment draft cleanup failed", "error", err)
			} else if cleaned > 0 {
				slog.Info("comment attachment draft cleanup completed", "count", cleaned)
			}
		}
	}
}
