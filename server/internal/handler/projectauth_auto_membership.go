package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-08-27 coder(lq): Keep business-event membership wiring in one adapter
// so upstream project, issue, and comment handlers only call a narrow helper.
func promoteProjectMemberWithExecutor(ctx context.Context, executor dbExecutor, projectID, userID string, role projectauth.ProjectRole) error {
	if executor == nil || projectID == "" || userID == "" {
		return nil
	}
	return projectauth.New(newProjectAuthRepository(executor), true).PromoteMember(ctx, projectID, userID, role)
}

func promoteMemberLeadWithExecutor(ctx context.Context, executor dbExecutor, projectID string, leadType pgtype.Text, leadID pgtype.UUID) error {
	if !leadType.Valid || leadType.String != "member" || !leadID.Valid {
		return nil
	}
	return promoteProjectMemberWithExecutor(ctx, executor, projectID, uuidToString(leadID), projectauth.ProjectOwner)
}

func promoteMentionedMembersWithExecutor(ctx context.Context, executor dbExecutor, projectID, content string) error {
	for _, mention := range util.ParseMentions(content) {
		if mention.Type != "member" {
			continue
		}
		if err := promoteProjectMemberWithExecutor(ctx, executor, projectID, mention.ID, projectauth.ProjectViewer); err != nil {
			return err
		}
	}
	return nil
}

func promoteIssueAccessWithExecutor(ctx context.Context, executor dbExecutor, projectID pgtype.UUID, assigneeType pgtype.Text, assigneeID pgtype.UUID, description pgtype.Text) error {
	if !projectID.Valid {
		return nil
	}
	projectIDString := uuidToString(projectID)
	if assigneeType.Valid && assigneeType.String == "member" && assigneeID.Valid {
		if err := promoteProjectMemberWithExecutor(ctx, executor, projectIDString, uuidToString(assigneeID), projectauth.ProjectMember); err != nil {
			return err
		}
	}
	if description.Valid {
		return promoteMentionedMembersWithExecutor(ctx, executor, projectIDString, description.String)
	}
	return nil
}

// 2026-08-27 coder(lq): All IssueService.Create transports use the same
// optional transaction hook, keeping projectauth out of the upstream service.
func (h *Handler) issueAccessBeforeCommit() func(context.Context, pgx.Tx, db.Issue) error {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, issue db.Issue) error {
		return promoteIssueAccessWithExecutor(ctx, tx, issue.ProjectID, issue.AssigneeType, issue.AssigneeID, issue.Description)
	}
}

// 2026-08-27 coder(lq): Ordinary issue updates do not otherwise need a
// transaction. Open one only while project authorization is enabled so the
// issue assignment/mention and its inherited project role cannot diverge.
func (h *Handler) updateIssueWithProjectAccess(ctx context.Context, workspaceID pgtype.UUID, statusKey string, params db.UpdateIssueParams) (db.Issue, error) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		var issue db.Issue
		err := h.runWithIssueStatusGuard(ctx, workspaceID, statusKey, func(q *db.Queries) error {
			var updateErr error
			issue, updateErr = q.UpdateIssue(ctx, params)
			return updateErr
		})
		return issue, err
	}
	if h.TxStarter == nil {
		return db.Issue{}, errors.New("project access issue update requires transaction starter")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.Issue{}, fmt.Errorf("begin project access issue update: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	if err := assertIssueStatusStillActive(ctx, qtx, workspaceID, statusKey); err != nil {
		return db.Issue{}, err
	}
	issue, err := qtx.UpdateIssue(ctx, params)
	if err != nil {
		return db.Issue{}, err
	}
	if err := promoteIssueAccessWithExecutor(ctx, tx, issue.ProjectID, issue.AssigneeType, issue.AssigneeID, issue.Description); err != nil {
		return db.Issue{}, fmt.Errorf("promote issue project access: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Issue{}, fmt.Errorf("commit project access issue update: %w", err)
	}
	return issue, nil
}

// 2026-08-27 coder(lq): Persist a human comment and viewer inheritance in one
// PostgreSQL transaction. Agent/squad mentions remain handled by the native
// trigger flow; this adapter only promotes member mentions.
func (h *Handler) createCommentWithProjectAccess(ctx context.Context, issue db.Issue, params db.CreateCommentParams) (db.CreateCommentRow, error) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() || !issue.ProjectID.Valid {
		return h.Queries.CreateComment(ctx, params)
	}
	if h.TxStarter == nil {
		return db.CreateCommentRow{}, errors.New("project access comment create requires transaction starter")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("begin project access comment create: %w", err)
	}
	defer tx.Rollback(ctx)

	created, err := h.Queries.WithTx(tx).CreateComment(ctx, params)
	if err != nil {
		return db.CreateCommentRow{}, err
	}
	if err := promoteMentionedMembersWithExecutor(ctx, tx, uuidToString(issue.ProjectID), params.Content); err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("promote comment mention project access: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("commit project access comment create: %w", err)
	}
	return created, nil
}
