package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Durable triggers use the same owner/explicit-share authority as human access.
func CanMemberInvokeAgent(ctx context.Context, queries *db.Queries, agent db.Agent, memberUserID pgtype.UUID, workspaceID pgtype.UUID) bool {
	userID := util.UUIDToString(memberUserID)
	if userID == "" {
		return false
	}
	_, err := queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: memberUserID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return false
	}
	if util.UUIDToString(agent.OwnerID) == userID {
		return true
	}
	if agent.PermissionMode != "public_to" {
		return false
	}
	targets, err := queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}
	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			return true
		case "member":
			if util.UUIDToString(t.TargetID) == userID {
				return true
			}
		}
	}
	return false
}

// CanTaskInvokeAgent rechecks current grants at the delivery boundary. Callers
// lock the agent row, serializing this decision with permission edits.
func CanTaskInvokeAgent(ctx context.Context, queries *db.Queries, agent db.Agent, task db.AgentTaskQueue) bool {
	if agent.Kind != "user" {
		return true
	}
	subject := AuthorizationSubject(task.OriginatorUserID, task.AccountableUserID)
	if subject.Valid {
		return CanMemberInvokeAgent(ctx, queries, agent, subject, agent.WorkspaceID)
	}
	if task.AutopilotRunID.Valid {
		run, err := queries.GetAutopilotRun(ctx, task.AutopilotRunID)
		if err != nil {
			return false
		}
		autopilot, err := queries.GetAutopilot(ctx, run.AutopilotID)
		if err != nil || autopilot.WorkspaceID != agent.WorkspaceID {
			return false
		}
		if autopilot.CreatedByType == "member" {
			return CanMemberInvokeAgent(ctx, queries, agent, autopilot.CreatedByID, agent.WorkspaceID)
		}
	}
	// Unattributed system work may only use workspace-shared agents.
	if agent.PermissionMode != "public_to" {
		return false
	}
	targets, err := queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}
	for _, target := range targets {
		if target.TargetType == "workspace" {
			return true
		}
	}
	return false
}
