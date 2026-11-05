package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-11-05 coder(lq): Keep the Agent's own assignment intact and mirror
// it to its human owner. A subscription is not a grant of task visibility.
func notifyAssignedAgentOwner(
	ctx context.Context,
	bus *events.Bus,
	queries *db.Queries,
	pool *pgxpool.Pool,
	e events.Event,
	issue handler.IssueResponse,
	canViewIssue issueViewAuthorizer,
) (ownerID string, delivered bool) {
	if canViewIssue == nil || issue.WorkspaceID != e.WorkspaceID ||
		issue.AssigneeType == nil || *issue.AssigneeType != "agent" || issue.AssigneeID == nil {
		return "", false
	}
	workspace, err := util.ParseUUID(issue.WorkspaceID)
	if err != nil {
		return "", false
	}
	agentID, err := util.ParseUUID(*issue.AssigneeID)
	if err != nil {
		return "", false
	}
	agent, err := queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspace})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("resolve assignee agent owner for inbox", "issue_id", issue.ID, "agent_id", *issue.AssigneeID, "error", err)
		}
		return "", false
	}
	ownerID = util.UUIDToString(agent.OwnerID)
	if ownerID == "" || !canDeliverIssueNotification(ctx, canViewIssue, issue.WorkspaceID, ownerID, issue.ID, "issue_assigned") {
		return "", false
	}

	// 2026-11-05 coder(lq): Use the direct subscriber tier for future updates,
	// but retain the owner's existing task and subtree unsubscribe choices.
	addMemberSubscriberRespectingOptOut(ctx, bus, pool, queries, issue.WorkspaceID, issue.ID, agent.OwnerID, "assignee")
	details, _ := json.Marshal(map[string]string{
		"new_assignee_type": "agent",
		"new_assignee_id":   util.UUIDToString(agent.ID),
	})
	delivered = notifyDirect(ctx, queries, bus, "member", ownerID, issue.WorkspaceID, e,
		issue.ID, issue.Status, "issue_assigned", "action_required", issue.Title, "", details, canViewIssue)
	return ownerID, delivered
}
