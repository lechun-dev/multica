package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const commentAttachmentDraftRetention = 24 * time.Hour

// 2026-09-03 coder(lq): Remove never-linked composer uploads object-first; the
// conditional SQL delete preserves rows linked by a concurrent comment edit.
func (s *TaskService) CleanupCommentAttachmentDrafts(ctx context.Context, limit int32) (int, error) {
	if s == nil || s.Queries == nil || s.CommentAttachmentStorage == nil || limit <= 0 {
		return 0, nil
	}
	drafts, err := s.Queries.ListStaleCommentAttachmentDrafts(ctx, db.ListStaleCommentAttachmentDraftsParams{
		CreatedBefore: pgtype.Timestamptz{Time: time.Now().Add(-commentAttachmentDraftRetention), Valid: true},
		RowLimit:      limit,
	})
	if err != nil {
		return 0, fmt.Errorf("list stale comment attachment drafts: %w", err)
	}
	cleaned := 0
	for _, draft := range drafts {
		if ctx.Err() != nil {
			return cleaned, nil
		}
		key := s.CommentAttachmentStorage.KeyFromURL(draft.Url)
		deleteCtx, cancel := context.WithTimeout(ctx, sourceContextObjectDeleteTimeout)
		deleteErr := s.CommentAttachmentStorage.DeleteObject(deleteCtx, key)
		cancel()
		if deleteErr != nil {
			continue
		}
		changed, err := s.Queries.DeleteCommentAttachmentDraft(ctx, db.DeleteCommentAttachmentDraftParams{
			ID: draft.ID, WorkspaceID: draft.WorkspaceID,
		})
		if err != nil {
			return cleaned, fmt.Errorf("delete stale comment attachment draft row: %w", err)
		}
		if changed == 1 {
			cleaned++
		}
	}
	return cleaned, nil
}
