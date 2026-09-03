package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-08-27 coder(lq): Agent-level cancellation is an aggregate mutation,
// but its rows can belong to different projects. Resolve authorization first,
// then perform one UPDATE ... RETURNING so chat cleanup/broadcast semantics
// stay identical to the upstream bulk path without changing generated SQL.
func (h *Handler) cancelAgentTasksWithProjectPermission(ctx context.Context, agentID pgtype.UUID, userID, workspaceID string) ([]db.AgentTaskQueue, error) {
	tasks, err := h.Queries.ListAgentTasks(ctx, agentID)
	if err != nil {
		return nil, err
	}
	allowedIDs := make([]pgtype.UUID, 0, len(tasks))
	member, err := h.getWorkspaceMember(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	subject := projectauth.Subject{UserID: userID, WorkspaceID: workspaceID, WorkspaceRole: projectauth.WorkspaceRole(member.Role)}
	for _, task := range tasks {
		switch {
		case task.IssueID.Valid:
			issue, issueErr := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: parseUUID(workspaceID)})
			if issueErr != nil {
				continue
			}
			// 2026-08-27 coder(lq): Once project permissions are enabled, every
			// task must resolve to a project that the caller can manage.
			if !issue.ProjectID.Valid {
				continue
			}
			if err := h.ProjectAuth.CheckIssue(ctx, subject, uuidToString(issue.ID), uuidToString(issue.ProjectID), projectauth.IssueManage); err == nil {
				allowedIDs = append(allowedIDs, task.ID)
			}
		case task.ChatSessionID.Valid:
			session, sessionErr := h.Queries.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: task.ChatSessionID, WorkspaceID: parseUUID(workspaceID)})
			if sessionErr != nil {
				continue
			}
			// 2026-08-27 coder(lq): Projectless chat tasks have no permission
			// scope and are hidden while the overlay is enabled.
			if !session.ProjectID.Valid {
				continue
			}
			if err := h.ProjectAuth.Check(ctx, subject, uuidToString(session.ProjectID), projectauth.IssueManage); err == nil {
				allowedIDs = append(allowedIDs, task.ID)
			}
		default:
			// 2026-08-27 coder(lq): Unscoped tasks cannot be authorized by the
			// project permission overlay.
			continue
		}
	}
	if len(allowedIDs) == 0 {
		return []db.AgentTaskQueue{}, nil
	}
	rows, err := h.DB.Query(ctx, `
		UPDATE agent_task_queue
		SET status = 'cancelled', completed_at = now(), prepare_lease_expires_at = NULL
		WHERE agent_id = $1
		  AND id = ANY($2::uuid[])
		  AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
		RETURNING *`, agentID, allowedIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[db.AgentTaskQueue])
}

// 2026-08-27 coder(lq): Aggregated agent counters must use the same project
// visibility boundary as task history. Keeping these SQL adapters here avoids
// changing upstream sqlc query contracts while preventing counts from becoming
// a side channel for inaccessible projects.
func (h *Handler) getWorkspaceAgentRunCountsWithProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID) ([]db.GetWorkspaceAgentRunCountsRow, error) {
	query := fmt.Sprintf(`SELECT atq.agent_id, COUNT(*)::int AS run_count
		FROM agent_task_queue atq
		JOIN agent a ON a.id = atq.agent_id
		WHERE a.workspace_id = $1
		  AND atq.created_at > now() - INTERVAL '30 days'
		  AND %s
		GROUP BY atq.agent_id`, projectVisibleTaskPredicate("atq", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]db.GetWorkspaceAgentRunCountsRow, 0)
	for rows.Next() {
		var row db.GetWorkspaceAgentRunCountsRow
		if err := rows.Scan(&row.AgentID, &row.RunCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (h *Handler) getWorkspaceAgentActivityWithProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID) ([]db.GetWorkspaceAgentActivity30dRow, error) {
	query := fmt.Sprintf(`SELECT atq.agent_id,
			DATE_TRUNC('day', atq.completed_at)::timestamptz AS bucket,
			COUNT(*)::int AS task_count,
			COUNT(*) FILTER (WHERE atq.status = 'failed')::int AS failed_count
		FROM agent_task_queue atq
		JOIN agent a ON a.id = atq.agent_id
		WHERE a.workspace_id = $1
		  AND atq.completed_at IS NOT NULL
		  AND atq.completed_at > now() - INTERVAL '30 days'
		  AND %s
		GROUP BY atq.agent_id, bucket
		ORDER BY atq.agent_id, bucket`, projectVisibleTaskPredicate("atq", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]db.GetWorkspaceAgentActivity30dRow, 0)
	for rows.Next() {
		var row db.GetWorkspaceAgentActivity30dRow
		if err := rows.Scan(&row.AgentID, &row.Bucket, &row.TaskCount, &row.FailedCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func projectVisibleTaskPredicate(taskAlias, workspaceRef, userRef string) string {
	return projectVisibleTaskPredicateWithWorkspaceScope(taskAlias, workspaceRef, userRef, true)
}

func projectVisibleTaskPredicateWithWorkspaceScope(taskAlias, workspaceRef, userRef string, includeWorkspaceOwned bool) string {
	ownerClause := workspaceOwnerBypassPredicate(workspaceRef)
	if !includeWorkspaceOwned {
		ownerClause = "FALSE"
	}
	return fmt.Sprintf(`(
		(%s.issue_id IS NOT NULL AND EXISTS (
		SELECT 1 FROM issue acl_issue
		WHERE acl_issue.id = %s.issue_id
		  AND acl_issue.workspace_id = %s
			AND %s
		))
		OR (%s.issue_id IS NULL AND %s.chat_session_id IS NOT NULL AND EXISTS (
		SELECT 1 FROM chat_session acl_chat
		WHERE acl_chat.id = %s.chat_session_id
		  AND acl_chat.workspace_id = %s
		  AND %s
		))
		OR (%s.issue_id IS NULL AND %s.chat_session_id IS NULL AND (
			(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'))
			OR %s.originator_user_id = %s::uuid
			OR %s.accountable_user_id = %s::uuid
			OR EXISTS (SELECT 1 FROM agent a WHERE a.id = %s.agent_id AND a.workspace_id = %s AND a.kind = 'user' AND a.owner_id = %s::uuid)
		))
	)`, taskAlias, taskAlias, workspaceRef,
		issueProjectVisibilityPredicateWithWorkspaceScope("acl_issue", workspaceRef, userRef, includeWorkspaceOwned),
		taskAlias, taskAlias, taskAlias, workspaceRef,
		chatProjectVisibilityPredicateWithWorkspaceScope("acl_chat", workspaceRef, userRef, includeWorkspaceOwned),
		taskAlias, taskAlias, ownerClause, workspaceRef, userRef,
		taskAlias, userRef, taskAlias, userRef,
		taskAlias, workspaceRef, userRef)
}

// 2026-08-28 coder(lq): Project-bound Chats inherit project visibility. When
// project permissions are enabled, projectless sessions are excluded so chat
// task rows cannot become an unscoped authorization side channel.
func chatProjectVisibilityPredicate(chatAlias, workspaceRef, userRef string) string {
	return chatProjectVisibilityPredicateWithWorkspaceScope(chatAlias, workspaceRef, userRef, true)
}

func chatProjectVisibilityPredicateWithWorkspaceScope(chatAlias, workspaceRef, userRef string, includeWorkspaceOwned bool) string {
	ownerProjectClause := "FALSE"
	if includeWorkspaceOwned {
		ownerProjectClause = fmt.Sprintf("(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'))", workspaceOwnerBypassPredicate(workspaceRef), workspaceRef, userRef)
	}
	return fmt.Sprintf(`(
		(%s.project_id IS NOT NULL AND (
			%s
			OR %s
		))
		OR (%s.project_id IS NULL AND (
			(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'))
			OR %s.creator_id = %s::uuid
			OR EXISTS (SELECT 1 FROM agent a WHERE a.id = %s.agent_id AND a.workspace_id = %s AND a.kind = 'user' AND a.owner_id = %s::uuid)
		))
	)`, chatAlias, ownerProjectClause, projectAccessPredicate(chatAlias+".project_id", workspaceRef, userRef),
		chatAlias, ownerProjectClause, workspaceRef, userRef, chatAlias, userRef,
		chatAlias, workspaceRef, userRef)
}

// 2026-08-28 coder(lq): Project-authenticated Chat lists must not inherit the
// upstream creator-only query. This adapter returns the same list projection
// while applying project and projectless visibility in SQL, so workspace
// owners and Agent owners can see sessions created by another member without
// changing sqlc-generated queries.
func (h *Handler) listChatSessionsWithProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID, includeArchived, includeWorkspaceOwned bool) ([]ChatSessionResponse, error) {
	query := fmt.Sprintf(`SELECT cs.id, cs.workspace_id, cs.agent_id, cs.creator_id, cs.title,
		cs.status, cs.created_at, cs.updated_at, cs.pinned_at, cs.project_id,
		CASE WHEN cs.status = 'archived' THEN 0
		     ELSE (SELECT count(*) FROM chat_message m
		             WHERE m.chat_session_id = cs.id
		               AND m.role = 'assistant'
		               AND m.created_at > cs.last_read_at)
		END::int AS unread_count,
		COALESCE(lm.content, '') AS last_message_content,
		COALESCE(lm.role, '') AS last_message_role,
		lm.created_at AS last_message_at,
		lm.failure_reason AS last_message_failure_reason,
		COALESCE(lm.message_kind, '') AS last_message_kind
	FROM chat_session cs
	LEFT JOIN LATERAL (
		SELECT content, role, created_at, failure_reason, message_kind
		FROM chat_message m
		WHERE m.chat_session_id = cs.id
		  AND m.message_kind != 'channel_command'
		ORDER BY m.created_at DESC
		LIMIT 1
	) lm ON true
	WHERE cs.workspace_id = $1
	  AND ($3::boolean OR cs.status = 'active')
	  AND %s
	  AND (cs.explicitly_created_at IS NOT NULL OR lm.created_at IS NOT NULL)
	ORDER BY (cs.pinned_at IS NOT NULL) DESC, cs.pinned_at DESC,
		         COALESCE(lm.created_at, cs.updated_at) DESC`, chatProjectVisibilityPredicateWithWorkspaceScope("cs", "$1", "$2", includeWorkspaceOwned))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ChatSessionResponse, 0)
	for rows.Next() {
		var row struct {
			ID                       pgtype.UUID
			WorkspaceID              pgtype.UUID
			AgentID                  pgtype.UUID
			CreatorID                pgtype.UUID
			Title                    string
			Status                   string
			CreatedAt                pgtype.Timestamptz
			UpdatedAt                pgtype.Timestamptz
			PinnedAt                 pgtype.Timestamptz
			ProjectID                pgtype.UUID
			UnreadCount              int32
			LastMessageContent       string
			LastMessageRole          string
			LastMessageAt            pgtype.Timestamptz
			LastMessageFailureReason pgtype.Text
			LastMessageKind          string
		}
		if err := rows.Scan(
			&row.ID, &row.WorkspaceID, &row.AgentID, &row.CreatorID, &row.Title,
			&row.Status, &row.CreatedAt, &row.UpdatedAt, &row.PinnedAt, &row.ProjectID,
			&row.UnreadCount, &row.LastMessageContent, &row.LastMessageRole,
			&row.LastMessageAt, &row.LastMessageFailureReason, &row.LastMessageKind,
		); err != nil {
			return nil, err
		}
		result = append(result, ChatSessionResponse{
			ID:          uuidToString(row.ID),
			WorkspaceID: uuidToString(row.WorkspaceID),
			AgentID:     uuidToString(row.AgentID),
			CreatorID:   uuidToString(row.CreatorID),
			ProjectID:   uuidToPtr(row.ProjectID),
			Title:       row.Title,
			Status:      row.Status,
			HasUnread:   row.UnreadCount > 0,
			UnreadCount: int(row.UnreadCount),
			LastMessage: buildChatLastMessage(row.LastMessageAt, row.LastMessageContent, row.LastMessageRole, row.LastMessageFailureReason, row.LastMessageKind),
			Pinned:      row.PinnedAt.Valid,
			CreatedAt:   timestampToString(row.CreatedAt),
			UpdatedAt:   timestampToString(row.UpdatedAt),
		})
	}
	return result, rows.Err()
}

// 2026-08-27 coder(lq): Keep the task projection rule pure so list and
// snapshot handlers share exactly the same treatment of unscoped tasks.
func taskVisibleByProjectPermission(task db.AgentTaskQueue, visibleIssueIDs, visibleChatSessionIDs map[pgtype.UUID]struct{}, visibleUnscopedTaskIDs ...map[pgtype.UUID]struct{}) bool {
	if task.IssueID.Valid {
		_, ok := visibleIssueIDs[task.IssueID]
		return ok
	}
	if task.ChatSessionID.Valid {
		_, ok := visibleChatSessionIDs[task.ChatSessionID]
		return ok
	}
	if len(visibleUnscopedTaskIDs) > 0 {
		_, ok := visibleUnscopedTaskIDs[0][task.ID]
		return ok
	}
	return false
}

func issueProjectVisibilityPredicate(issueAlias, workspaceRef, userRef string) string {
	return issueProjectVisibilityPredicateWithWorkspaceScope(issueAlias, workspaceRef, userRef, true)
}

func issueProjectVisibilityPredicateWithWorkspaceScope(issueAlias, workspaceRef, userRef string, includeWorkspaceOwned bool) string {
	// 2026-09-03 coder(lq): Project-bound tasks use canonical project grants;
	// projectless Agent tasks retain the upstream creator/assignee visibility.
	ownerProjectClause := "FALSE"
	ownerProjectlessClause := "FALSE"
	if includeWorkspaceOwned {
		ownerProjectClause = fmt.Sprintf("(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'))", workspaceOwnerBypassPredicate(workspaceRef), workspaceRef, userRef)
		ownerProjectlessClause = ownerProjectClause
	}
	return fmt.Sprintf(`(
		(%s.project_id IS NOT NULL AND (%s OR %s))
		OR (%s.project_id IS NULL AND (
			(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'))
			OR (%s.creator_type = 'member' AND %s.creator_id = %s::uuid)
			OR (%s.assignee_type = 'member' AND %s.assignee_id = %s::uuid)
			OR (%s.creator_type = 'agent' AND EXISTS (
				SELECT 1 FROM agent a WHERE a.id = %s.creator_id AND a.workspace_id = %s AND a.kind = 'user' AND a.owner_id = %s::uuid
			))
			OR (%s.assignee_type = 'agent' AND EXISTS (
				SELECT 1 FROM agent a WHERE a.id = %s.assignee_id AND a.workspace_id = %s AND a.kind = 'user' AND a.owner_id = %s::uuid
			))
		))
	)`, issueAlias, ownerProjectClause, projectAccessPredicate(issueAlias+".project_id", workspaceRef, userRef),
		issueAlias, ownerProjectlessClause, workspaceRef, userRef,
		issueAlias, issueAlias, userRef,
		issueAlias, issueAlias, userRef,
		issueAlias, issueAlias, workspaceRef, userRef,
		issueAlias, issueAlias, workspaceRef, userRef)
}

// 2026-08-31 coder(lq): Keep project-list visibility in one SQL adapter so
// project, task, chat and dashboard queries use the same authorization fact.
// The predicate intentionally resolves only project.view. Task-level grants
// are not considered here, which prevents a task share from exposing the
// remainder of its project.
func projectAccessPredicate(projectExpr, workspaceRef, userRef string) string {
	// 2026-09-01 coder(lq): Once the overlay is enabled, project visibility is
	// derived exclusively from the canonical grant table. The native member
	// table remains the source for workspace-owner bypass, but a legacy project
	// membership row must never make a project visible by itself.
	// 2026-09-01 coder(lq): Keep the principal expression identical for the
	// grant being evaluated and the grant that assigns a project role. This
	// avoids user/org/everyone drift and keeps every boolean operator grouped.
	principal := func(alias string) string {
		return fmt.Sprintf(`(
			(%s.subject_type = 'user' AND %s.subject_id = a.user_id::text)
			OR (%s.subject_type = 'everyone' AND (%s.subject_id = '' OR %s.subject_id = a.workspace_id::text))
			OR (%s.subject_type = 'organization' AND %s.subject_id IN (
				SELECT om.organization_id::text
				FROM projectauth_organization_members om
				JOIN projectauth_organizations org ON org.id = om.organization_id
				WHERE om.workspace_id = a.workspace_id
				  AND om.user_id = a.user_id
				  AND org.status = 'active'
			))
		)`, alias, alias, alias, alias, alias, alias, alias)
	}
	grantSubject := principal("g")
	roleHolder := principal("rg")
	return fmt.Sprintf(`EXISTS (
		WITH auth_subject AS (
			SELECT %s::uuid AS workspace_id, %s::uuid AS user_id
		)
		SELECT 1
		FROM project p
		CROSS JOIN auth_subject a
		WHERE p.id = %s
		  AND p.workspace_id = a.workspace_id
		  AND EXISTS (
			SELECT 1
			FROM projectauth_access_grants g
			WHERE g.workspace_id = p.workspace_id
			  AND g.project_id = p.id
			  AND g.issue_id IS NULL
			  AND (
				%s
				OR (g.subject_type = 'role' AND EXISTS (
					SELECT 1
					FROM projectauth_access_grants rg
					WHERE rg.workspace_id = p.workspace_id
					  AND rg.project_id = p.id
					  AND rg.issue_id IS NULL
					  AND rg.role_key IS NOT NULL
					  AND %s
					  AND (rg.role_key = g.subject_id OR (g.subject_id = '' AND rg.role_key = g.role_key))
				))
			  )
			  AND (
				g.permission = 'project.view'
				OR (g.role_key IS NOT NULL AND EXISTS (
					SELECT 1
					FROM project_permission_roles rr
					JOIN project_permission_role_permissions rp ON rp.role_id = rr.id
					WHERE rr.workspace_id = p.workspace_id
					  AND rr.role_key = g.role_key
					  AND rp.permission = 'project.view'
				))
			  )
		)
	)`, workspaceRef, userRef, projectExpr, grantSubject, roleHolder)
}

// workspaceOwnerBypassPredicate is embedded into all SQL visibility scopes so
// project lists, issue lists, chats, and task queues share one interpretation
// of the workspace-level owner switch. The switch now comes from the process
// environment, not workspace.settings, so deployment config is the only source
// of truth.
// 2026-09-01 coder(lq): Centralize the SQL fragment to prevent one list path
// from accidentally retaining unconditional workspace-owner visibility.
func workspaceOwnerBypassPredicate(workspaceRef string) string {
	_ = workspaceRef
	if os.Getenv("PROJECT_OWNER_BYPASS_ENABLED") == "false" {
		return "FALSE"
	}
	return "TRUE"
}

// 2026-09-02 coder(lq): Projectless tasks remain visible only to their creator,
// assignee, or an enabled workspace-owner bypass, but participants must still
// be able to move the work forward before a project is selected. Once a
// project_id is supplied, UpdateIssue performs a separate target-project
// permission check before binding the task, so allowing mutation here cannot
// bypass the project ACL.
func projectlessIssuePermissionAllowed(issue db.Issue, userID pgtype.UUID, workspaceRole projectauth.WorkspaceRole, permission projectauth.Permission) bool {
	return projectlessIssuePermissionAllowedWithOwners(issue, userID, workspaceRole, permission, pgtype.UUID{}, pgtype.UUID{})
}

func projectlessIssuePermissionAllowedWithOwners(issue db.Issue, userID pgtype.UUID, workspaceRole projectauth.WorkspaceRole, permission projectauth.Permission, creatorOwnerID, assigneeOwnerID pgtype.UUID) bool {
	return projectlessIssuePermissionAllowedWithOwnersAndBypass(issue, userID, workspaceRole, permission, true, creatorOwnerID, assigneeOwnerID)
}

func projectlessIssuePermissionAllowedWithOwnersAndBypass(issue db.Issue, userID pgtype.UUID, workspaceRole projectauth.WorkspaceRole, permission projectauth.Permission, ownerBypassEnabled bool, creatorOwnerID, assigneeOwnerID pgtype.UUID) bool {
	if workspaceRole == projectauth.WorkspaceOwner && ownerBypassEnabled {
		return true
	}
	if !userID.Valid {
		return false
	}
	if issue.CreatorType == "member" && issue.CreatorID.Valid && issue.CreatorID == userID {
		return true
	}
	if issue.CreatorType == "agent" && creatorOwnerID.Valid && creatorOwnerID == userID {
		return true
	}
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "member" && issue.AssigneeID.Valid && issue.AssigneeID == userID {
		return true
	}
	return issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" && assigneeOwnerID.Valid && assigneeOwnerID == userID
}

// 2026-08-27 coder(lq): Dashboard rollups need a project boundary even when
// no project_id is supplied. Keep this predicate in the Handler adapter so
// the upstream sqlc queries remain untouched and the overlay can be removed
// without carrying a forked generated contract.
func dashboardProjectVisibilityPredicate(projectExpr, workspaceRef, userRef string) string {
	// 2026-09-01 coder(lq): Dashboard aggregates are another indirect
	// project-list surface. Keep workspace-owner bypass and canonical project
	// grants in the same predicate so an aggregate cannot be probed by UUID or
	// exposed through the old project_members table.
	ownerClause := fmt.Sprintf(`EXISTS (
		SELECT 1 FROM member m
		WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'
	)`, workspaceRef, userRef)
	return fmt.Sprintf(`(%s IS NOT NULL AND (%s OR %s))`,
		projectExpr, ownerClause, projectAccessPredicate(projectExpr, workspaceRef, userRef))
}

func (h *Handler) dashboardNeedsProjectFilter(projectID pgtype.UUID) bool {
	return h.ProjectAuth != nil && h.ProjectAuth.Enabled() && !projectID.Valid
}

// 2026-08-27 coder(lq): A dashboard project_id is a project-level read and
// must be checked before the generated query runs, otherwise a member could
// probe an inaccessible project's aggregates by supplying its UUID.
func (h *Handler) requireDashboardProjectView(w http.ResponseWriter, r *http.Request, workspaceID string, projectID pgtype.UUID) bool {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() || !projectID.Valid {
		return true
	}
	return h.requireProjectPermission(w, r, uuidToString(projectID), workspaceID, projectauth.View)
}

func (h *Handler) listDashboardUsageDailyWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID, userID, tz string, since pgtype.Timestamptz) ([]db.ListDashboardUsageDailyRow, error) {
	query := fmt.Sprintf(`SELECT
		DATE(bucket_hour AT TIME ZONE $3::text) AS date,
		LOWER(provider) AS provider,
		model,
		SUM(input_tokens)::bigint,
		SUM(output_tokens)::bigint,
		SUM(cache_read_tokens)::bigint,
		SUM(cache_write_tokens)::bigint,
		SUM(cost_usd_ticks)::bigint,
		SUM(COALESCE(uncosted_input_tokens, input_tokens))::bigint,
		SUM(COALESCE(uncosted_output_tokens, output_tokens))::bigint,
		SUM(COALESCE(uncosted_cache_read_tokens, cache_read_tokens))::bigint,
		SUM(COALESCE(uncosted_cache_write_tokens, cache_write_tokens))::bigint,
		SUM(task_count)::int
	FROM task_usage_hourly
	WHERE workspace_id = $1
	  AND bucket_hour >= $4::timestamptz
	  AND %s
	GROUP BY DATE(bucket_hour AT TIME ZONE $3::text), LOWER(provider), model
	ORDER BY DATE(bucket_hour AT TIME ZONE $3::text) DESC, LOWER(provider), model`, dashboardProjectVisibilityPredicate("task_usage_hourly.project_id", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, tz, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListDashboardUsageDailyRow, 0)
	for rows.Next() {
		var row db.ListDashboardUsageDailyRow
		if err := rows.Scan(&row.Date, &row.Provider, &row.Model, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens, &row.CostUsdTicks, &row.UncostedInputTokens, &row.UncostedOutputTokens, &row.UncostedCacheReadTokens, &row.UncostedCacheWriteTokens, &row.TaskCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) listDashboardUsageByAgentWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID, userID string, since pgtype.Timestamptz) ([]db.ListDashboardUsageByAgentRow, error) {
	query := fmt.Sprintf(`SELECT
		agent_id,
		LOWER(provider) AS provider,
		model,
		SUM(input_tokens)::bigint,
		SUM(output_tokens)::bigint,
		SUM(cache_read_tokens)::bigint,
		SUM(cache_write_tokens)::bigint,
		SUM(cost_usd_ticks)::bigint,
		SUM(COALESCE(uncosted_input_tokens, input_tokens))::bigint,
		SUM(COALESCE(uncosted_output_tokens, output_tokens))::bigint,
		SUM(COALESCE(uncosted_cache_read_tokens, cache_read_tokens))::bigint,
		SUM(COALESCE(uncosted_cache_write_tokens, cache_write_tokens))::bigint,
		SUM(task_count)::int
	FROM task_usage_hourly
	WHERE workspace_id = $1
	  AND bucket_hour >= $3::timestamptz
	  AND %s
	GROUP BY agent_id, LOWER(provider), model
	ORDER BY agent_id, LOWER(provider), model`, dashboardProjectVisibilityPredicate("task_usage_hourly.project_id", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListDashboardUsageByAgentRow, 0)
	for rows.Next() {
		var row db.ListDashboardUsageByAgentRow
		if err := rows.Scan(&row.AgentID, &row.Provider, &row.Model, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens, &row.CostUsdTicks, &row.UncostedInputTokens, &row.UncostedOutputTokens, &row.UncostedCacheReadTokens, &row.UncostedCacheWriteTokens, &row.TaskCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) listDashboardAgentRunTimeWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID, userID string, since pgtype.Timestamptz) ([]db.ListDashboardAgentRunTimeRow, error) {
	query := fmt.Sprintf(`SELECT
		atq.agent_id,
		COALESCE(SUM(EXTRACT(EPOCH FROM (atq.completed_at - atq.started_at)))::bigint, 0)::bigint,
		COUNT(*)::int,
		COUNT(*) FILTER (WHERE atq.status = 'failed')::int,
		COUNT(*) FILTER (WHERE atq.status = 'cancelled')::int
	FROM agent_task_queue atq
	JOIN agent a ON a.id = atq.agent_id
	LEFT JOIN issue i ON i.id = atq.issue_id
	WHERE a.workspace_id = $1
	  AND atq.status IN ('completed', 'failed', 'cancelled')
	  AND atq.started_at IS NOT NULL
	  AND atq.completed_at IS NOT NULL
	  AND atq.completed_at >= $3::timestamptz
	  AND %s
	GROUP BY atq.agent_id
	ORDER BY total_seconds DESC`, dashboardProjectVisibilityPredicate("i.project_id", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListDashboardAgentRunTimeRow, 0)
	for rows.Next() {
		var row db.ListDashboardAgentRunTimeRow
		if err := rows.Scan(&row.AgentID, &row.TotalSeconds, &row.TaskCount, &row.FailedCount, &row.CancelledCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) listDashboardRunTimeDailyWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID, userID, tz string, since pgtype.Timestamptz) ([]db.ListDashboardRunTimeDailyRow, error) {
	query := fmt.Sprintf(`SELECT
		DATE(atq.completed_at AT TIME ZONE $3::text),
		COALESCE(SUM(EXTRACT(EPOCH FROM (atq.completed_at - atq.started_at)))::bigint, 0)::bigint,
		COUNT(*)::int,
		COUNT(*) FILTER (WHERE atq.status = 'failed')::int,
		COUNT(*) FILTER (WHERE atq.status = 'cancelled')::int
	FROM agent_task_queue atq
	JOIN agent a ON a.id = atq.agent_id
	LEFT JOIN issue i ON i.id = atq.issue_id
	WHERE a.workspace_id = $1
	  AND atq.status IN ('completed', 'failed', 'cancelled')
	  AND atq.started_at IS NOT NULL
	  AND atq.completed_at IS NOT NULL
	  AND atq.completed_at >= $4::timestamptz
	  AND %s
	GROUP BY DATE(atq.completed_at AT TIME ZONE $3::text)
	ORDER BY DATE(atq.completed_at AT TIME ZONE $3::text) DESC`, dashboardProjectVisibilityPredicate("i.project_id", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, tz, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListDashboardRunTimeDailyRow, 0)
	for rows.Next() {
		var row db.ListDashboardRunTimeDailyRow
		if err := rows.Scan(&row.Date, &row.TotalSeconds, &row.TaskCount, &row.FailedCount, &row.CancelledCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) listDashboardFailuresDailyWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID, userID, tz string, since pgtype.Timestamptz) ([]db.ListDashboardFailuresDailyRow, error) {
	query := fmt.Sprintf(`SELECT
		DATE(atq.completed_at AT TIME ZONE $3::text),
		CASE WHEN atq.status = 'failed' THEN COALESCE(NULLIF(atq.failure_reason, ''), 'unclassified') ELSE '' END,
		COUNT(*)::int
	FROM agent_task_queue atq
	JOIN agent a ON a.id = atq.agent_id
	LEFT JOIN issue i ON i.id = atq.issue_id
	WHERE a.workspace_id = $1
	  AND atq.status IN ('completed', 'failed')
	  AND atq.completed_at IS NOT NULL
	  AND atq.completed_at >= $4::timestamptz
	  AND %s
	GROUP BY 1, 2
	ORDER BY 1 DESC, 2`, dashboardProjectVisibilityPredicate("i.project_id", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, tz, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListDashboardFailuresDailyRow, 0)
	for rows.Next() {
		var row db.ListDashboardFailuresDailyRow
		if err := rows.Scan(&row.Date, &row.FailureReason, &row.TaskCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) listDashboardFailuresByAgentWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID, userID string, since pgtype.Timestamptz) ([]db.ListDashboardFailuresByAgentRow, error) {
	query := fmt.Sprintf(`SELECT
		atq.agent_id,
		CASE WHEN atq.status = 'failed' THEN COALESCE(NULLIF(atq.failure_reason, ''), 'unclassified') ELSE '' END,
		COUNT(*)::int
	FROM agent_task_queue atq
	JOIN agent a ON a.id = atq.agent_id
	LEFT JOIN issue i ON i.id = atq.issue_id
	WHERE a.workspace_id = $1
	  AND atq.status IN ('completed', 'failed')
	  AND atq.completed_at IS NOT NULL
	  AND atq.completed_at >= $3::timestamptz
	  AND %s
	GROUP BY atq.agent_id, 2
	ORDER BY atq.agent_id, 2`, dashboardProjectVisibilityPredicate("i.project_id", "$1", "$2"))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListDashboardFailuresByAgentRow, 0)
	for rows.Next() {
		var row db.ListDashboardFailuresByAgentRow
		if err := rows.Scan(&row.AgentID, &row.FailureReason, &row.TaskCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// 2026-08-27 coder(lq): Runtime reports are aggregate read paths, so applying
// project View at the endpoint alone is insufficient: the aggregate itself
// must exclude rows from projects outside the caller's scope. These adapters
// intentionally live beside the project-auth boundary and leave the upstream
// runtime_usage sqlc queries unchanged for low-friction upgrades.
func (h *Handler) listRuntimeUsageWithProjectPermission(ctx context.Context, runtimeID, workspaceID, userID pgtype.UUID, tz string, since pgtype.Timestamptz) ([]db.ListRuntimeUsageRow, error) {
	query := fmt.Sprintf(`SELECT
		DATE(bucket_hour AT TIME ZONE $4::text) AS date,
		LOWER(provider) AS provider,
		model,
		SUM(input_tokens)::bigint,
		SUM(output_tokens)::bigint,
		SUM(cache_read_tokens)::bigint,
		SUM(cache_write_tokens)::bigint,
		SUM(cost_usd_ticks)::bigint,
		SUM(COALESCE(uncosted_input_tokens, input_tokens))::bigint,
		SUM(COALESCE(uncosted_output_tokens, output_tokens))::bigint,
		SUM(COALESCE(uncosted_cache_read_tokens, cache_read_tokens))::bigint,
		SUM(COALESCE(uncosted_cache_write_tokens, cache_write_tokens))::bigint
	FROM task_usage_hourly
	WHERE runtime_id = $1
	  AND bucket_hour >= $5::timestamptz
	  AND %s
	GROUP BY DATE(bucket_hour AT TIME ZONE $4::text), LOWER(provider), model
	ORDER BY DATE(bucket_hour AT TIME ZONE $4::text) DESC, LOWER(provider), model`, dashboardProjectVisibilityPredicate("task_usage_hourly.project_id", "$2", "$3"))
	rows, err := h.DB.Query(ctx, query, runtimeID, workspaceID, userID, tz, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListRuntimeUsageRow, 0)
	for rows.Next() {
		var row db.ListRuntimeUsageRow
		if err := rows.Scan(&row.Date, &row.Provider, &row.Model, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens, &row.CostUsdTicks, &row.UncostedInputTokens, &row.UncostedOutputTokens, &row.UncostedCacheReadTokens, &row.UncostedCacheWriteTokens); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) getRuntimeTaskHourlyActivityWithProjectPermission(ctx context.Context, runtimeID, workspaceID, userID pgtype.UUID, tz string) ([]db.GetRuntimeTaskHourlyActivityRow, error) {
	query := fmt.Sprintf(`SELECT EXTRACT(HOUR FROM atq.started_at AT TIME ZONE $4::text)::int AS hour,
		COUNT(*)::int AS count
	FROM agent_task_queue atq
	WHERE atq.runtime_id = $1
	  AND atq.started_at IS NOT NULL
	  AND %s
	GROUP BY hour
	ORDER BY hour`, projectVisibleTaskPredicate("atq", "$2", "$3"))
	rows, err := h.DB.Query(ctx, query, runtimeID, workspaceID, userID, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.GetRuntimeTaskHourlyActivityRow, 0)
	for rows.Next() {
		var row db.GetRuntimeTaskHourlyActivityRow
		if err := rows.Scan(&row.Hour, &row.Count); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) listRuntimeUsageByAgentWithProjectPermission(ctx context.Context, runtimeID, workspaceID, userID pgtype.UUID, since pgtype.Timestamptz) ([]db.ListRuntimeUsageByAgentRow, error) {
	query := fmt.Sprintf(`SELECT
		atq.agent_id,
		LOWER(tu.provider) AS provider,
		tu.model,
		SUM(tu.input_tokens)::bigint,
		SUM(tu.output_tokens)::bigint,
		SUM(tu.cache_read_tokens)::bigint,
		SUM(tu.cache_write_tokens)::bigint,
		COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint,
		COALESCE(SUM(tu.input_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COALESCE(SUM(tu.output_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COALESCE(SUM(tu.cache_read_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COALESCE(SUM(tu.cache_write_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COUNT(DISTINCT tu.task_id)::int
	FROM task_usage tu
	JOIN agent_task_queue atq ON atq.id = tu.task_id
	WHERE atq.runtime_id = $1
	  AND tu.created_at >= $4::timestamptz
	  AND %s
	GROUP BY atq.agent_id, LOWER(tu.provider), tu.model
	ORDER BY atq.agent_id, LOWER(tu.provider), tu.model`, projectVisibleTaskPredicate("atq", "$2", "$3"))
	rows, err := h.DB.Query(ctx, query, runtimeID, workspaceID, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListRuntimeUsageByAgentRow, 0)
	for rows.Next() {
		var row db.ListRuntimeUsageByAgentRow
		if err := rows.Scan(&row.AgentID, &row.Provider, &row.Model, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens, &row.CostUsdTicks, &row.UncostedInputTokens, &row.UncostedOutputTokens, &row.UncostedCacheReadTokens, &row.UncostedCacheWriteTokens, &row.TaskCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (h *Handler) getRuntimeUsageByHourWithProjectPermission(ctx context.Context, runtimeID, workspaceID, userID pgtype.UUID, tz string, since pgtype.Timestamptz) ([]db.GetRuntimeUsageByHourRow, error) {
	query := fmt.Sprintf(`SELECT
		EXTRACT(HOUR FROM tu.created_at AT TIME ZONE $4::text)::int,
		tu.model,
		SUM(tu.input_tokens)::bigint,
		SUM(tu.output_tokens)::bigint,
		SUM(tu.cache_read_tokens)::bigint,
		SUM(tu.cache_write_tokens)::bigint,
		COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint,
		COALESCE(SUM(tu.input_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COALESCE(SUM(tu.output_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COALESCE(SUM(tu.cache_read_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COALESCE(SUM(tu.cache_write_tokens) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint,
		COUNT(DISTINCT tu.task_id)::int
	FROM task_usage tu
	JOIN agent_task_queue atq ON atq.id = tu.task_id
	WHERE atq.runtime_id = $1
	  AND tu.created_at >= $5::timestamptz
	  AND %s
	GROUP BY EXTRACT(HOUR FROM tu.created_at AT TIME ZONE $4::text), tu.model
	ORDER BY hour, tu.model`, projectVisibleTaskPredicate("atq", "$2", "$3"))
	rows, err := h.DB.Query(ctx, query, runtimeID, workspaceID, userID, tz, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.GetRuntimeUsageByHourRow, 0)
	for rows.Next() {
		var row db.GetRuntimeUsageByHourRow
		if err := rows.Scan(&row.Hour, &row.Model, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens, &row.CostUsdTicks, &row.UncostedInputTokens, &row.UncostedOutputTokens, &row.UncostedCacheReadTokens, &row.UncostedCacheWriteTokens, &row.TaskCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// 2026-08-27 coder(lq): Notifications carry only an issue id, so resolve the
// issue's project in one SQL predicate before returning or counting it. This
// keeps inbox authorization in the Handler adapter while the policy package
// remains independent of the upstream sqlc models.
func inboxIssueProjectVisibilityPredicate(inboxAlias, workspaceRef, userRef string) string {
	return inboxIssueProjectVisibilityPredicateWithWorkspaceScope(inboxAlias, workspaceRef, userRef, true)
}

// 2026-09-01 coder(lq): Inbox counters and summaries must use the same
// workspace-owner scope as the inbox list, otherwise unread badges can reveal
// activity from projects hidden by the owner toggle.
func inboxIssueProjectVisibilityPredicateWithWorkspaceScope(inboxAlias, workspaceRef, userRef string, includeWorkspaceOwned bool) string {
	return fmt.Sprintf(`(%s.issue_id IS NULL OR EXISTS (
		SELECT 1 FROM issue acl_issue
		WHERE acl_issue.id = %s.issue_id
		  AND acl_issue.workspace_id = %s
		  AND %s
	))`, inboxAlias, inboxAlias, workspaceRef,
		issueProjectVisibilityPredicateWithWorkspaceScope("acl_issue", workspaceRef, userRef, includeWorkspaceOwned))
}

// 2026-09-02 coder(lq): Archived issues stay out of live inbox counts, but
// bare system notifications with no issue_id still count as normal.
func inboxIssueNotArchivedPredicate(inboxAlias string) string {
	return fmt.Sprintf(`(%s.issue_id IS NULL OR EXISTS (
		SELECT 1 FROM issue active_issue
		WHERE active_issue.id = %s.issue_id
		  AND active_issue.archived_at IS NULL
	))`, inboxAlias, inboxAlias)
}

// 2026-08-27 coder(lq): Batch the project View check for issue ids used by
// inbox and pin endpoints. The one round trip avoids an authorization N+1
// while preserving the same workspace-owner/admin and project-member rules.
func (h *Handler) visibleIssueIDsByProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID, issueIDs []pgtype.UUID) (map[pgtype.UUID]struct{}, error) {
	return h.visibleIssueIDsByProjectPermissionWithWorkspaceScope(ctx, workspaceID, userID, issueIDs, true)
}

// 2026-09-01 coder(lq): Keep the task-page owner toggle effective on every
// batched issue read path, including open-only lists and child hydration.
func (h *Handler) visibleIssueIDsByProjectPermissionWithWorkspaceScope(ctx context.Context, workspaceID, userID pgtype.UUID, issueIDs []pgtype.UUID, includeWorkspaceOwned bool) (map[pgtype.UUID]struct{}, error) {
	visible := make(map[pgtype.UUID]struct{}, len(issueIDs))
	if len(issueIDs) == 0 || h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return visible, nil
	}
	query := fmt.Sprintf(`SELECT requested.id
		FROM unnest($2::uuid[]) requested(id)
		JOIN issue i ON i.id = requested.id AND i.workspace_id = $1
		WHERE %s`, issueProjectVisibilityPredicateWithWorkspaceScope("i", "$1", "$3", includeWorkspaceOwned))
	rows, err := h.DB.Query(ctx, query, workspaceID, issueIDs, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	return visible, rows.Err()
}

// 2026-08-27 coder(lq): Filter task projections in one authorization pass so
// history and presence endpoints cannot expose runs outside the caller's
// View scope. Issue-backed tasks use the same project/projectless predicate as
// issue lists; chat-only tasks use the corresponding Chat visibility rule.
func (h *Handler) filterTasksByProjectPermission(ctx context.Context, workspaceID, userID string, tasks []db.AgentTaskQueue) ([]db.AgentTaskQueue, error) {
	return h.filterTasksByProjectPermissionWithWorkspaceScope(ctx, workspaceID, userID, tasks, true)
}

// 2026-09-01 coder(lq): Keep the task-page owner toggle effective for every
// task projection, while the wrapper preserves compatibility for callers that
// do not carry a task-page view setting.
func (h *Handler) filterTasksByProjectPermissionWithWorkspaceScope(ctx context.Context, workspaceID, userID string, tasks []db.AgentTaskQueue, includeWorkspaceOwned bool) ([]db.AgentTaskQueue, error) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() || len(tasks) == 0 {
		return tasks, nil
	}
	if userID == "" {
		return []db.AgentTaskQueue{}, nil
	}
	issueIDs := make([]pgtype.UUID, 0, len(tasks))
	chatSessionIDs := make([]pgtype.UUID, 0, len(tasks))
	taskIDs := make([]pgtype.UUID, 0, len(tasks))
	seen := make(map[pgtype.UUID]struct{}, len(tasks))
	seenChats := make(map[pgtype.UUID]struct{}, len(tasks))
	for _, task := range tasks {
		if task.ID.Valid {
			taskIDs = append(taskIDs, task.ID)
		}
		if task.IssueID.Valid {
			if _, ok := seen[task.IssueID]; !ok {
				seen[task.IssueID] = struct{}{}
				issueIDs = append(issueIDs, task.IssueID)
			}
		}
		if task.ChatSessionID.Valid {
			if _, ok := seenChats[task.ChatSessionID]; !ok {
				seenChats[task.ChatSessionID] = struct{}{}
				chatSessionIDs = append(chatSessionIDs, task.ChatSessionID)
			}
		}
	}
	visible, err := h.visibleIssueIDsByProjectPermissionWithWorkspaceScope(ctx, parseUUID(workspaceID), parseUUID(userID), issueIDs, includeWorkspaceOwned)
	if err != nil {
		return nil, err
	}
	visibleChats, err := h.visibleChatSessionIDsByProjectPermissionWithWorkspaceScope(ctx, parseUUID(workspaceID), parseUUID(userID), chatSessionIDs, includeWorkspaceOwned)
	if err != nil {
		return nil, err
	}
	visibleUnscoped, err := h.visibleUnscopedTaskIDsByProjectPermissionWithWorkspaceScope(ctx, parseUUID(workspaceID), parseUUID(userID), taskIDs, includeWorkspaceOwned)
	if err != nil {
		return nil, err
	}
	filtered := make([]db.AgentTaskQueue, 0, len(tasks))
	for _, task := range tasks {
		if taskVisibleByProjectPermission(task, visible, visibleChats, visibleUnscoped) {
			filtered = append(filtered, task)
		}
	}
	return filtered, nil
}

// 2026-08-28 coder(lq): Unscoped queue rows have no Issue/ChatSession row to
// join. Authorize them from their human attribution or the owning user of the
// executing Agent, while keeping the workspace boundary in SQL.
func (h *Handler) visibleUnscopedTaskIDsByProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID, taskIDs []pgtype.UUID) (map[pgtype.UUID]struct{}, error) {
	return h.visibleUnscopedTaskIDsByProjectPermissionWithWorkspaceScope(ctx, workspaceID, userID, taskIDs, true)
}

// 2026-09-01 coder(lq): Apply the same owner-scope switch to projectless
// queue rows used by task history and agent snapshots.
func (h *Handler) visibleUnscopedTaskIDsByProjectPermissionWithWorkspaceScope(ctx context.Context, workspaceID, userID pgtype.UUID, taskIDs []pgtype.UUID, includeWorkspaceOwned bool) (map[pgtype.UUID]struct{}, error) {
	visible := make(map[pgtype.UUID]struct{}, len(taskIDs))
	if len(taskIDs) == 0 || h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return visible, nil
	}
	// 2026-09-01 coder(lq): Apply the workspace-owner bypass switch to
	// projectless queue rows as well; otherwise task history could leak rows
	// after an owner disables workspace-wide project access.
	ownerClause := workspaceOwnerBypassPredicate("$1")
	if !includeWorkspaceOwned {
		ownerClause = "FALSE"
	}
	rows, err := h.DB.Query(ctx, fmt.Sprintf(`
		SELECT atq.id
		FROM unnest($3::uuid[]) requested(id)
		JOIN agent_task_queue atq ON atq.id = requested.id
		JOIN agent a ON a.id = atq.agent_id AND a.workspace_id = $1
		WHERE atq.issue_id IS NULL AND atq.chat_session_id IS NULL
		  AND (
			(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = $1 AND m.user_id = $2 AND m.role = 'owner'))
			OR atq.originator_user_id = $2
			OR atq.accountable_user_id = $2
			OR (a.kind = 'user' AND a.owner_id = $2)
		  )`, ownerClause), workspaceID, userID, taskIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	return visible, rows.Err()
}

// 2026-08-28 coder(lq): The upstream pending-chat query is creator-scoped,
// which is too narrow once project and projectless visibility are enabled.
// Load the workspace set here and let the shared permission adapter filter it
// by project, Chat creator, and Agent owner without changing sqlc output.
func (h *Handler) listPendingChatTasksWithProjectPermission(ctx context.Context, workspaceID pgtype.UUID) ([]db.ListPendingChatTasksByCreatorRow, error) {
	rows, err := h.DB.Query(ctx, `
		SELECT atq.id AS task_id, atq.status, atq.chat_session_id, cs.agent_id
		FROM agent_task_queue atq
		JOIN chat_session cs ON cs.id = atq.chat_session_id
		WHERE atq.chat_session_id IS NOT NULL
		  AND atq.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
		  AND atq.regenerate_quick_actions_for IS NULL
		  AND cs.workspace_id = $1
		ORDER BY atq.created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]db.ListPendingChatTasksByCreatorRow, 0)
	for rows.Next() {
		var row db.ListPendingChatTasksByCreatorRow
		if err := rows.Scan(&row.TaskID, &row.Status, &row.ChatSessionID, &row.AgentID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// 2026-08-27 coder(lq): Resolve chat-session visibility in one query to avoid
// an authorization round trip per task. Projectless Chat sessions use their
// creator, Agent-owner, and workspace-owner visibility rule.
func (h *Handler) visibleChatSessionIDsByProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID, sessionIDs []pgtype.UUID) (map[pgtype.UUID]struct{}, error) {
	return h.visibleChatSessionIDsByProjectPermissionWithWorkspaceScope(ctx, workspaceID, userID, sessionIDs, true)
}

// 2026-09-01 coder(lq): Carry the task-page owner toggle into chat-backed
// task projections as well as issue-backed projections.
func (h *Handler) visibleChatSessionIDsByProjectPermissionWithWorkspaceScope(ctx context.Context, workspaceID, userID pgtype.UUID, sessionIDs []pgtype.UUID, includeWorkspaceOwned bool) (map[pgtype.UUID]struct{}, error) {
	visible := make(map[pgtype.UUID]struct{}, len(sessionIDs))
	if len(sessionIDs) == 0 || h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return visible, nil
	}
	query := fmt.Sprintf(`SELECT requested.id
		FROM unnest($2::uuid[]) requested(id)
		JOIN chat_session cs ON cs.id = requested.id AND cs.workspace_id = $1
		WHERE %s`, chatProjectVisibilityPredicateWithWorkspaceScope("cs", "$1", "$3", includeWorkspaceOwned))
	rows, err := h.DB.Query(ctx, query, workspaceID, sessionIDs, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	return visible, rows.Err()
}

// 2026-08-27 coder(lq): The workspace-working-agents projection exposes one
// aggregate row per agent, but its upstream shape only carries issue IDs.
// For chat/mixed rows, verify every running task in the row is visible so a
// hidden project chat cannot leak the agent name or running count.
func (h *Handler) visibleWorkingAgentIDsByProjectPermission(ctx context.Context, workspaceID, userID pgtype.UUID, agentIDs []pgtype.UUID) (map[pgtype.UUID]struct{}, error) {
	return h.visibleWorkingAgentIDsByProjectPermissionWithWorkspaceScope(ctx, workspaceID, userID, agentIDs, true)
}

// 2026-09-01 coder(lq): The working-agents projection is a task-page
// side-channel, so it must honor the same workspace-owner bypass setting as
// the primary task list.
func (h *Handler) visibleWorkingAgentIDsByProjectPermissionWithWorkspaceScope(ctx context.Context, workspaceID, userID pgtype.UUID, agentIDs []pgtype.UUID, includeWorkspaceOwned bool) (map[pgtype.UUID]struct{}, error) {
	visible := make(map[pgtype.UUID]struct{}, len(agentIDs))
	if len(agentIDs) == 0 || h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return visible, nil
	}
	query := fmt.Sprintf(`SELECT atq.agent_id
		FROM agent_task_queue atq
		JOIN agent a ON a.id = atq.agent_id AND a.workspace_id = $1
		WHERE atq.agent_id = ANY($3::uuid[]) AND atq.status = 'running'
		GROUP BY atq.agent_id
		HAVING COUNT(*) FILTER (WHERE %s) = COUNT(*)`, projectVisibleTaskPredicateWithWorkspaceScope("atq", "$1", "$2", includeWorkspaceOwned))
	rows, err := h.DB.Query(ctx, query, workspaceID, userID, agentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	return visible, rows.Err()
}

// 2026-08-27 coder(lq): Pending chat rows do not include their task's issue
// or session project in the upstream query shape. Resolve those links here so
// the aggregate FAB endpoints apply the same project boundary as transcript
// reads; tasks without a project-bound issue or chat session remain hidden.
func (h *Handler) filterPendingChatTasksByProjectPermissionWithWorkspaceScope(ctx context.Context, workspaceID, userID string, rows []db.ListPendingChatTasksByCreatorRow, includeWorkspaceOwned bool) ([]db.ListPendingChatTasksByCreatorRow, error) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() || len(rows) == 0 {
		return rows, nil
	}
	issueIDs := make([]pgtype.UUID, 0, len(rows))
	chatSessionIDs := make([]pgtype.UUID, 0, len(rows))
	taskIDs := make([]pgtype.UUID, 0, len(rows))
	tasks := make([]db.AgentTaskQueue, len(rows))
	for i, row := range rows {
		task, taskErr := h.Queries.GetAgentTask(ctx, row.TaskID)
		if taskErr != nil {
			return nil, taskErr
		}
		tasks[i] = task
		if task.ID.Valid {
			taskIDs = append(taskIDs, task.ID)
		}
		if task.IssueID.Valid {
			issueIDs = append(issueIDs, task.IssueID)
		}
		if task.ChatSessionID.Valid {
			chatSessionIDs = append(chatSessionIDs, task.ChatSessionID)
		}
	}
	visibleIssues, err := h.visibleIssueIDsByProjectPermissionWithWorkspaceScope(ctx, parseUUID(workspaceID), parseUUID(userID), issueIDs, includeWorkspaceOwned)
	if err != nil {
		return nil, err
	}
	visibleChats, err := h.visibleChatSessionIDsByProjectPermissionWithWorkspaceScope(ctx, parseUUID(workspaceID), parseUUID(userID), chatSessionIDs, includeWorkspaceOwned)
	if err != nil {
		return nil, err
	}
	visibleUnscoped, err := h.visibleUnscopedTaskIDsByProjectPermissionWithWorkspaceScope(ctx, parseUUID(workspaceID), parseUUID(userID), taskIDs, includeWorkspaceOwned)
	if err != nil {
		return nil, err
	}
	filtered := make([]db.ListPendingChatTasksByCreatorRow, 0, len(rows))
	for i, row := range rows {
		// 2026-08-27 coder(lq): Keep pending-chat aggregation identical to
		// task history: issue context wins when both links exist, while
		// projectless chats and unscoped tasks use their creator/Agent-owner rule.
		if !taskVisibleByProjectPermission(tasks[i], visibleIssues, visibleChats, visibleUnscoped) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered, nil
}

func (h *Handler) filterPendingChatTasksByProjectPermission(ctx context.Context, workspaceID, userID string, rows []db.ListPendingChatTasksByCreatorRow) ([]db.ListPendingChatTasksByCreatorRow, error) {
	return h.filterPendingChatTasksByProjectPermissionWithWorkspaceScope(ctx, workspaceID, userID, rows, true)
}

// 2026-08-27 coder(lq): Project pins use the same visibility scope as project
// lists. Returning a set lets callers filter mixed pin types without probing
// each project individually.
func (h *Handler) visibleProjectIDSet(ctx context.Context, workspaceID, userID string) (map[pgtype.UUID]struct{}, error) {
	visible := make(map[pgtype.UUID]struct{})
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return visible, nil
	}
	ids, err := h.ProjectAuth.Scope(ctx, projectauth.Subject{UserID: userID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		parsed := parseUUID(id)
		if parsed.Valid {
			visible[parsed] = struct{}{}
		}
	}
	return visible, nil
}

// 2026-08-24 coder(lq): Keep HTTP/error mapping in this thin adapter so the
// independent projectauth policy stays free of chi and generated DB models.
func (h *Handler) requireProjectPermission(w http.ResponseWriter, r *http.Request, projectID, workspaceID string, permission projectauth.Permission) bool {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return true
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return false
	}
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return false
	}
	member, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return false
	}
	subject := projectauth.Subject{
		UserID:        userID,
		WorkspaceID:   workspaceID,
		WorkspaceRole: projectauth.WorkspaceRole(member.Role),
	}
	if err := h.ProjectAuth.RequireWithWorkspaceScope(r.Context(), subject, projectID, permission, includeWorkspaceOwnedFromRequest(r)); err != nil {
		// Project membership is intentionally indistinguishable from a missing
		// project to avoid leaking project IDs across the workspace boundary.
		if errors.Is(err, projectauth.ErrMigrationRequired) {
			writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_migration_required", "project permission migration is required")
		} else if errors.Is(err, projectauth.ErrStorageUnavailable) || errors.Is(err, projectauth.ErrDisabled) {
			writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_unavailable", "project permission storage is unavailable")
		} else if errors.Is(err, projectauth.ErrNotWorkspaceMember) || errors.Is(err, projectauth.ErrNoProjectAccess) {
			writeError(w, http.StatusNotFound, "project not found")
		} else if errors.Is(err, projectauth.ErrForbidden) {
			writeErrorCode(w, http.StatusForbidden, "project_permission_forbidden", "insufficient project permissions")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to check project permissions")
		}
		return false
	}
	return true
}

// 2026-08-27 coder(lq): Project-bound tasks inherit the issue's project;
// projectless tasks are visible only to their creator, member assignee, or
// workspace owner while the project-permission overlay is on.
func (h *Handler) requireIssueProjectPermission(w http.ResponseWriter, r *http.Request, issue db.Issue, permission projectauth.Permission) bool {
	ok, reason := h.issueProjectAllowedWithWorkspaceScope(r, issue, permission, includeWorkspaceOwnedFromRequest(r))
	if ok {
		return true
	}
	if reason == "projectless" {
		writeError(w, http.StatusNotFound, "task is not attached to a project")
	} else if reason == "migration" {
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_migration_required", "project permission migration is required")
	} else if reason == "unavailable" {
		writeErrorCode(w, http.StatusServiceUnavailable, "project_permission_unavailable", "project permission storage is unavailable")
	} else if reason == "internal" {
		writeError(w, http.StatusInternalServerError, "failed to check project permissions")
	} else if reason == "forbidden" {
		writeErrorCode(w, http.StatusForbidden, "project_permission_forbidden", "insufficient project permissions")
	} else {
		writeError(w, http.StatusNotFound, "project not found")
	}
	return false
}

// 2026-08-24 coder(lq): Allow list handlers to filter unauthorized tasks
// without writing an HTTP response halfway through a successful page.
func (h *Handler) issueProjectAllowed(r *http.Request, issue db.Issue, permission projectauth.Permission) (bool, string) {
	return h.issueProjectAllowedWithWorkspaceScope(r, issue, permission, true)
}

// 2026-09-01 coder(lq): Query visibility is intentionally one-way: false can
// only remove the workspace-owner bypass, never grant additional access.
func includeWorkspaceOwnedFromRequest(r *http.Request) bool {
	return r.URL.Query().Get("include_workspace_owned") != "false"
}

func (h *Handler) issueProjectAllowedWithWorkspaceScope(r *http.Request, issue db.Issue, permission projectauth.Permission, includeWorkspaceOwned bool) (bool, string) {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return true, ""
	}
	userID := requestUserID(r)
	if userID == "" {
		return false, "denied"
	}
	member, err := h.getWorkspaceMember(r.Context(), userID, uuidToString(issue.WorkspaceID))
	if err != nil {
		return false, "denied"
	}
	if !issue.ProjectID.Valid {
		userUUID, parseErr := util.ParseUUID(userID)
		if parseErr != nil {
			return false, "denied"
		}
		var creatorOwnerID, assigneeOwnerID pgtype.UUID
		if issue.CreatorType == "agent" && issue.CreatorID.Valid {
			ownerID, resolveErr := resolveAgentOwnerInWorkspaceWithExecutor(r.Context(), h.DB, uuidToString(issue.WorkspaceID), uuidToString(issue.CreatorID))
			if resolveErr != nil {
				return false, "internal"
			}
			if ownerID != "" {
				creatorOwnerID, _ = util.ParseUUID(ownerID)
			}
		}
		if issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid {
			ownerID, resolveErr := resolveAgentOwnerInWorkspaceWithExecutor(r.Context(), h.DB, uuidToString(issue.WorkspaceID), uuidToString(issue.AssigneeID))
			if resolveErr != nil {
				return false, "internal"
			}
			if ownerID != "" {
				assigneeOwnerID, _ = util.ParseUUID(ownerID)
			}
		}
		ownerBypassEnabled, bypassErr := h.ProjectAuth.WorkspaceOwnerBypassEnabled(r.Context(), uuidToString(issue.WorkspaceID))
		if bypassErr != nil {
			return false, "internal"
		}
		ownerBypassEnabled = ownerBypassEnabled && includeWorkspaceOwned
		if projectlessIssuePermissionAllowedWithOwnersAndBypass(issue, userUUID, projectauth.WorkspaceRole(member.Role), permission, ownerBypassEnabled, creatorOwnerID, assigneeOwnerID) {
			return true, ""
		}
		return false, "projectless"
	}
	subject := projectauth.Subject{UserID: userID, WorkspaceID: uuidToString(issue.WorkspaceID), WorkspaceRole: projectauth.WorkspaceRole(member.Role)}
	err = h.ProjectAuth.CheckIssueWithWorkspaceScope(r.Context(), subject, uuidToString(issue.ID), uuidToString(issue.ProjectID), permission, includeWorkspaceOwned)
	if err != nil {
		if errors.Is(err, projectauth.ErrMigrationRequired) {
			return false, "migration"
		}
		if errors.Is(err, projectauth.ErrStorageUnavailable) || errors.Is(err, projectauth.ErrDisabled) {
			return false, "unavailable"
		}
		if errors.Is(err, projectauth.ErrForbidden) {
			return false, "forbidden"
		}
		return false, "denied"
	}
	return true, ""
}

// 2026-08-24 coder(lq): Agent runs are task side effects, so they inherit the
// same project boundary as the issue and additionally require AgentUse.
func (h *Handler) issueAgentAllowed(r *http.Request, issue db.Issue) bool {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return true
	}
	allowed, _ := h.issueProjectAllowed(r, issue, projectauth.AgentUse)
	return allowed
}

func (h *Handler) requireNewIssueProjectPermission(w http.ResponseWriter, r *http.Request, workspaceID string, projectID pgtype.UUID, permission projectauth.Permission) bool {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return true
	}
	if !projectID.Valid {
		// 2026-08-31 coder(lq): The authorization overlay makes project binding
		// mandatory. Without a canonical project there is no safe inheritance
		// boundary for task visibility or direct grants. The disabled path above
		// deliberately preserves Multica's legacy projectless behavior.
		writeError(w, http.StatusBadRequest, "project_id is required when project permissions are enabled")
		return false
	}
	return h.requireProjectPermission(w, r, uuidToString(projectID), workspaceID, permission)
}

// 2026-08-24 coder(lq): Parent-child links are task access edges too. When the
// overlay is enabled, a parent must be visible to the caller and both tasks
// must stay in the same project; otherwise a user could use a visible child
// to discover or mutate an unrelated project's task tree.
func (h *Handler) requireParentIssueProjectPermission(w http.ResponseWriter, r *http.Request, parent db.Issue, projectID pgtype.UUID) bool {
	if h.ProjectAuth == nil || !h.ProjectAuth.Enabled() {
		return true
	}
	if !projectID.Valid || !parent.ProjectID.Valid || parent.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "parent issue must belong to the same project")
		return false
	}
	return h.requireIssueProjectPermission(w, r, parent, projectauth.View)
}
