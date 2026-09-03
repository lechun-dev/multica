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

func resolveIssueAssigneeUserWithExecutor(ctx context.Context, executor dbExecutor, projectID string, assigneeType pgtype.Text, assigneeID pgtype.UUID) (string, error) {
	if !assigneeType.Valid || !assigneeID.Valid {
		return "", nil
	}
	switch assigneeType.String {
	case "member":
		return uuidToString(assigneeID), nil
	case "agent":
		return resolveAgentOwnerWithExecutor(ctx, executor, projectID, uuidToString(assigneeID))
	default:
		return "", nil
	}
}

// 2026-09-01 coder(lq): Reconcile mention grants from the complete issue
// surface (description plus every comment). Grants are source=system, so a
// removed mention can be revoked safely without touching manual task shares;
// keeping the aggregate set also avoids revoking a user mentioned elsewhere
// on the same task.
func syncIssueMentionAccessWithExecutor(ctx context.Context, executor dbExecutor, issueID, projectID, description string) error {
	desired := make(map[string]struct{})
	addMentions := func(content string) error {
		for _, mention := range util.ParseMentions(content) {
			if mention.Type != "member" && mention.Type != "agent" {
				continue
			}
			userID := mention.ID
			if mention.Type == "agent" {
				var err error
				userID, err = resolveAgentOwnerWithExecutor(ctx, executor, projectID, mention.ID)
				if err != nil {
					return err
				}
			}
			if userID != "" {
				desired[userID] = struct{}{}
			}
		}
		return nil
	}
	if err := addMentions(description); err != nil {
		return err
	}
	rows, err := executor.Query(ctx, `SELECT content FROM comment WHERE issue_id=$1`, issueID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			rows.Close()
			return err
		}
		if err := addMentions(content); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	currentRows, err := executor.Query(ctx, `
		SELECT subject_id FROM projectauth_access_grants
		WHERE issue_id=$1 AND project_id=$2 AND subject_type='user'
		  AND permission=$3 AND source='system'`, issueID, projectID, string(projectauth.View))
	if err != nil {
		return err
	}
	var current []string
	for currentRows.Next() {
		var userID string
		if err := currentRows.Scan(&userID); err != nil {
			currentRows.Close()
			return err
		}
		current = append(current, userID)
	}
	if err := currentRows.Err(); err != nil {
		currentRows.Close()
		return err
	}
	currentRows.Close()
	for _, userID := range current {
		if _, keep := desired[userID]; keep {
			continue
		}
		if _, err := executor.Exec(ctx, `DELETE FROM projectauth_access_grants
			WHERE issue_id=$1 AND project_id=$2 AND subject_type='user' AND subject_id=$3
			  AND permission=$4 AND source='system'`, issueID, projectID, userID, string(projectauth.View)); err != nil {
			return err
		}
	}
	for userID := range desired {
		if err := upsertIssueAccessGrant(ctx, executor, issueID, projectID, userID, projectauth.View); err != nil {
			return err
		}
	}
	return nil
}

// 2026-09-01 coder(lq): Synchronize automatic task grants after every issue
// write. Assignee Edit grants are revoked when the assignee changes; mention
// View grants are reconciled against current issue/comment content. Manual
// grants remain untouched because only source=system rows are removed.
func syncIssueAccessWithExecutor(ctx context.Context, executor dbExecutor, previous *db.Issue, issue db.Issue) error {
	if previous != nil && previous.ProjectID.Valid && (!issue.ProjectID.Valid || previous.ProjectID != issue.ProjectID) {
		_, err := executor.Exec(ctx, `DELETE FROM projectauth_access_grants WHERE issue_id=$1 AND source='system'`, uuidToString(issue.ID))
		if err != nil {
			return err
		}
	}
	if !issue.ProjectID.Valid {
		return nil
	}
	projectID := uuidToString(issue.ProjectID)
	issueID := uuidToString(issue.ID)
	currentAssignee, err := resolveIssueAssigneeUserWithExecutor(ctx, executor, projectID, issue.AssigneeType, issue.AssigneeID)
	if err != nil {
		return err
	}
	previousAssignee := ""
	if previous != nil && previous.ProjectID.Valid && previous.ProjectID == issue.ProjectID {
		previousAssignee, err = resolveIssueAssigneeUserWithExecutor(ctx, executor, projectID, previous.AssigneeType, previous.AssigneeID)
		if err != nil {
			return err
		}
	}
	if previousAssignee != "" && previousAssignee != currentAssignee {
		if _, err := executor.Exec(ctx, `DELETE FROM projectauth_access_grants
			WHERE issue_id=$1 AND project_id=$2 AND subject_type='user' AND subject_id=$3
			  AND permission=$4 AND source='system'`, issueID, projectID, previousAssignee, string(projectauth.Edit)); err != nil {
			return err
		}
	}
	if currentAssignee != "" {
		if err := upsertIssueAccessGrant(ctx, executor, issueID, projectID, currentAssignee, projectauth.Edit); err != nil {
			return err
		}
	}
	return syncIssueMentionAccessWithExecutor(ctx, executor, issueID, projectID, issue.Description.String)
}

// 2026-08-31 coder(lq): Keep automatic assignee/mention grants mirrored into
// the unified source while legacy issue_permissions remains available for
// rollback and older handlers.
func upsertIssueAccessGrant(ctx context.Context, executor dbExecutor, issueID, projectID, userID string, permission projectauth.Permission) error {
	if _, err := executor.Exec(ctx, `
		INSERT INTO issue_permissions (issue_id, project_id, user_id, permission, granted_by)
		VALUES ($1,$2,$3,$4,$3)
		ON CONFLICT (issue_id,user_id,permission) DO NOTHING`, issueID, projectID, userID, string(permission)); err != nil {
		return err
	}
	_, err := executor.Exec(ctx, `
		INSERT INTO projectauth_access_grants (workspace_id, project_id, issue_id, subject_type, subject_id, permission, source, granted_by)
		SELECT p.workspace_id, $2, $1, 'user', $3, $4, 'system', $3
		FROM project p WHERE p.id=$2
		ON CONFLICT DO NOTHING`, issueID, projectID, userID, string(permission))
	return err
}

// 2026-08-27 coder(lq): All IssueService.Create transports use the same
// optional transaction hook, keeping projectauth out of the upstream service.
func (h *Handler) issueAccessBeforeCommit() func(context.Context, pgx.Tx, db.Issue) error {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, issue db.Issue) error {
		return syncIssueAccessWithExecutor(ctx, tx, nil, issue)
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
	previous, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: params.ID, WorkspaceID: workspaceID})
	if err != nil {
		return db.Issue{}, err
	}
	issue, err := qtx.UpdateIssue(ctx, params)
	if err != nil {
		return db.Issue{}, err
	}
	if err := syncIssueAccessWithExecutor(ctx, tx, &previous, issue); err != nil {
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
	if err := syncIssueMentionAccessWithExecutor(ctx, tx, uuidToString(issue.ID), uuidToString(issue.ProjectID), issue.Description.String); err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("promote comment mention project access: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.CreateCommentRow{}, fmt.Errorf("commit project access comment create: %w", err)
	}
	return created, nil
}
