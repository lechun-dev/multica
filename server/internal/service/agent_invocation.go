package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-10-09 coder(lq): Durable triggers use human visibility too; an owner or
// admin can invoke any agent they can view, just like interactive entry points.
func CanMemberInvokeAgent(ctx context.Context, queries *db.Queries, agent db.Agent, memberUserID pgtype.UUID, workspaceID pgtype.UUID) bool {
	allowed, err := memberMayInvokeAgent(ctx, queries, agent, memberUserID, workspaceID)
	return err == nil && allowed
}

// memberMayInvokeAgent is the policy itself, with query failures kept apart
// from denials.
//
// (false, nil) is a real verdict: no user id, not the owner of a private
// agent, or a public_to agent this member is not a target of — including the
// member row simply not being there, which is what pgx.ErrNoRows means here.
//
// (false, err) is "we do not know". Collapsing the two is what turns one
// transient database error into a message the platform will never redeliver
// and a person told they lack a permission they have.
func memberMayInvokeAgent(ctx context.Context, queries *db.Queries, agent db.Agent, memberUserID pgtype.UUID, workspaceID pgtype.UUID) (bool, error) {
	userID := util.UUIDToString(memberUserID)
	if userID == "" {
		return false, nil
	}
	member, err := queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: memberUserID, WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load workspace member: %w", err)
	}
	if member.Role == "owner" || member.Role == "admin" {
		return true, nil
	}
	if util.UUIDToString(agent.OwnerID) == userID {
		return true, nil
	}
	if agent.PermissionMode != "public_to" {
		return false, nil
	}
	targets, err := queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false, fmt.Errorf("list agent invocation targets: %w", err)
	}
	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			return true, nil
		case "member":
			if util.UUIDToString(t.TargetID) == userID {
				return true, nil
			}
		}
	}
	return false, nil
}
