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

// 2026-08-28 coder(lq): Agents are permission aliases for their owning user.
// Resolve through the project workspace in the same query so an agent from a
// different workspace can never create a project grant accidentally.
func resolveAgentOwnerWithExecutor(ctx context.Context, executor dbExecutor, projectID, agentID string) (string, error) {
	if executor == nil || projectID == "" || agentID == "" {
		return "", nil
	}
	projectUUID, err := util.ParseUUID(projectID)
	if err != nil {
		return "", nil
	}
	agentUUID, err := util.ParseUUID(agentID)
	if err != nil {
		return "", nil
	}
	var ownerID pgtype.UUID
	err = executor.QueryRow(ctx, `
		SELECT a.owner_id
		FROM agent a
		JOIN project p ON p.workspace_id = a.workspace_id
		WHERE a.id = $1 AND p.id = $2 AND a.kind = 'user'`, agentUUID, projectUUID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if !ownerID.Valid {
		return "", nil
	}
	return uuidToString(ownerID), nil
}

// 2026-08-28 coder(lq): Projectless issues still treat an Agent as its owning
// user, but there is no project row to anchor the lookup. Keep the workspace
// predicate explicit so an Agent from another workspace cannot grant access.
func resolveAgentOwnerInWorkspaceWithExecutor(ctx context.Context, executor dbExecutor, workspaceID, agentID string) (string, error) {
	if executor == nil || workspaceID == "" || agentID == "" {
		return "", nil
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return "", nil
	}
	agentUUID, err := util.ParseUUID(agentID)
	if err != nil {
		return "", nil
	}
	var ownerID pgtype.UUID
	err = executor.QueryRow(ctx, `
		SELECT owner_id
		FROM agent
		WHERE id = $1 AND workspace_id = $2 AND kind = 'user'`, agentUUID, workspaceUUID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if !ownerID.Valid {
		return "", nil
	}
	return uuidToString(ownerID), nil
}

func promoteMemberLeadWithExecutor(ctx context.Context, executor dbExecutor, projectID string, leadType pgtype.Text, leadID pgtype.UUID) error {
	if !leadType.Valid || !leadID.Valid {
		return nil
	}
	leadUserID := ""
	switch leadType.String {
	case "member":
		leadUserID = uuidToString(leadID)
	case "agent":
		var err error
		leadUserID, err = resolveAgentOwnerWithExecutor(ctx, executor, projectID, uuidToString(leadID))
		if err != nil {
			return err
		}
	default:
		return nil
	}
	return promoteProjectMemberWithExecutor(ctx, executor, projectID, leadUserID, projectauth.ProjectOwner)
}

func promoteMentionedMembersWithExecutor(ctx context.Context, executor dbExecutor, projectID, content string) error {
	for _, mention := range util.ParseMentions(content) {
		userID := ""
		switch mention.Type {
		case "member":
			userID = mention.ID
		case "agent":
			var err error
			userID, err = resolveAgentOwnerWithExecutor(ctx, executor, projectID, mention.ID)
			if err != nil {
				return err
			}
		default:
			continue
		}
		if err := promoteProjectMemberWithExecutor(ctx, executor, projectID, userID, projectauth.ProjectViewer); err != nil {
			return err
		}
	}
	return nil
}

// 2026-09-05 coder(lq): A mention makes the recipient a member of this task
// only. Grant conversation access alongside visibility; do not add a
// project_members row because that would expose every task in the project.
func promoteIssueMentionedMembersWithExecutor(ctx context.Context, executor dbExecutor, issueID, projectID, content string) error {
	for _, mention := range util.ParseMentions(content) {
		userID := mention.ID
		if mention.Type == "agent" {
			var err error
			userID, err = resolveAgentOwnerWithExecutor(ctx, executor, projectID, mention.ID)
			if err != nil {
				return err
			}
		}
		if mention.Type != "member" && mention.Type != "agent" || userID == "" {
			continue
		}
		for _, permission := range []string{"project.view", "project.issue.comment"} {
			if _, err := executor.Exec(ctx, `INSERT INTO issue_permissions (issue_id, project_id, user_id, permission, granted_by) VALUES ($1,$2,$3,$4,$3) ON CONFLICT (issue_id,user_id,permission) DO NOTHING`, issueID, projectID, userID, permission); err != nil {
				return err
			}
		}
	}
	return nil
}

func promoteIssueAccessWithExecutor(ctx context.Context, executor dbExecutor, issueID pgtype.UUID, projectID pgtype.UUID, assigneeType pgtype.Text, assigneeID pgtype.UUID, description pgtype.Text) error {
	if !projectID.Valid {
		return nil
	}
	projectIDString := uuidToString(projectID)
	if assigneeType.Valid && assigneeID.Valid {
		assigneeUserID := ""
		switch assigneeType.String {
		case "member":
			assigneeUserID = uuidToString(assigneeID)
		case "agent":
			var err error
			assigneeUserID, err = resolveAgentOwnerWithExecutor(ctx, executor, projectIDString, uuidToString(assigneeID))
			if err != nil {
				return err
			}
		default:
			assigneeUserID = ""
		}
		if assigneeUserID != "" {
			if _, err := executor.Exec(ctx, `INSERT INTO issue_permissions (issue_id, project_id, user_id, permission, granted_by) VALUES ($1,$2,$3,'project.edit',$3) ON CONFLICT (issue_id,user_id,permission) DO NOTHING`, issueID, projectID, assigneeUserID); err != nil {
				return err
			}
		}
	}
	if description.Valid {
		return promoteIssueMentionedMembersWithExecutor(ctx, executor, uuidToString(issueID), projectIDString, description.String)
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
		return promoteIssueAccessWithExecutor(ctx, tx, issue.ID, issue.ProjectID, issue.AssigneeType, issue.AssigneeID, issue.Description)
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
	if err := promoteIssueAccessWithExecutor(ctx, tx, issue.ID, issue.ProjectID, issue.AssigneeType, issue.AssigneeID, issue.Description); err != nil {
		return db.Issue{}, fmt.Errorf("promote issue project access: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Issue{}, fmt.Errorf("commit project access issue update: %w", err)
	}
	return issue, nil
}

// 2026-08-27 coder(lq): Persist a human comment and viewer inheritance in one
// PostgreSQL transaction. Native comment triggers still handle agent/squad
// execution; this adapter also maps Agent mentions to their owner's viewer
// grant without creating a separate Agent permission record.
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
	if err := promoteIssueMentionedMembersWithExecutor(ctx, tx, uuidToString(issue.ID), uuidToString(issue.ProjectID), params.Content); err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("promote comment mention project access: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("commit project access comment create: %w", err)
	}
	return created, nil
}
