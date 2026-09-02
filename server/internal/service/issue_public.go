package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrIssueRevisionConflict means the caller based an update on a stale issue
// revision. Transports map it to their stable conflict response.
var ErrIssueRevisionConflict = errors.New("issue revision conflict")

// IssueContentPatch is the first shared Public API write primitive. It is
// deliberately transport- and credential-agnostic: App, PAT, and Plugin
// entrypoints authorize independently, then call the same business operation.
type IssueContentPatch struct {
	Title            *string
	Description      *string
	ExpectedRevision *int64
	// BeforeCommit lets an authorized transport persist narrowly-related
	// overlay state atomically without coupling this upstream service to it.
	// 2026-08-27 coder(lq): Project mention inheritance uses this hook.
	BeforeCommit func(context.Context, pgx.Tx, db.Issue) error
}

// UpdateContent updates only the low-risk issue content fields exposed in the
// first Public API slice. Assignment, status, project, and hierarchy changes
// remain separate operations because each has additional policy and side
// effects.
func (s *IssueService) UpdateContent(ctx context.Context, issue db.Issue, patch IssueContentPatch) (db.Issue, error) {
	params := db.UpdateIssueParams{
		ID:            issue.ID,
		AssigneeType:  issue.AssigneeType,
		AssigneeID:    issue.AssigneeID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
		Stage:         issue.Stage,
	}
	if patch.ExpectedRevision != nil {
		params.ExpectedRevision = pgtype.Int8{Int64: *patch.ExpectedRevision, Valid: true}
	}
	if patch.Title != nil {
		params.Title = pgtype.Text{String: *patch.Title, Valid: true}
	}
	if patch.Description != nil {
		params.Description = pgtype.Text{String: *patch.Description, Valid: true}
	}

	if patch.BeforeCommit == nil {
		updated, err := s.Queries.UpdateIssue(ctx, params)
		if patch.ExpectedRevision != nil && errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, ErrIssueRevisionConflict
		}
		return updated, err
	}
	if s.TxStarter == nil {
		return db.Issue{}, errors.New("issue content hook requires transaction starter")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.Issue{}, fmt.Errorf("begin issue content update: %w", err)
	}
	defer tx.Rollback(ctx)

	updated, err := s.Queries.WithTx(tx).UpdateIssue(ctx, params)
	if patch.ExpectedRevision != nil && errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, ErrIssueRevisionConflict
	}
	if err != nil {
		return db.Issue{}, err
	}
	if err := patch.BeforeCommit(ctx, tx, updated); err != nil {
		return db.Issue{}, fmt.Errorf("before issue content commit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Issue{}, fmt.Errorf("commit issue content update: %w", err)
	}
	return updated, nil
}
