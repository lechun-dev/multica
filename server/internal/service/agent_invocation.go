package service

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-09 coder(lq): Durable triggers use human visibility too; an owner or
// admin can invoke any agent they can view, just like interactive entry points.
func CanMemberInvokeAgent(ctx context.Context, queries *db.Queries, agent db.Agent, memberUserID pgtype.UUID, workspaceID pgtype.UUID) bool {
	userID := util.UUIDToString(memberUserID)
	if userID == "" {
		return false
	}
	member, err := queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: memberUserID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return false
	}
	if member.Role == "owner" || member.Role == "admin" {
		return true
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
