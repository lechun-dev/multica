package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-09-02 coder(lq): Public API transports share this service boundary, so
// an archived task must be rejected before any query or transaction can run.
func TestUpdateContentRejectsArchivedIssue(t *testing.T) {
	title := "must not be written"
	service := &IssueService{}

	_, err := service.UpdateContent(context.Background(), db.Issue{
		ArchivedAt: pgtype.Timestamptz{Valid: true},
	}, IssueContentPatch{Title: &title})
	if !errors.Is(err, ErrArchivedIssue) {
		t.Fatalf("UpdateContent archived error = %v, want ErrArchivedIssue", err)
	}
}
