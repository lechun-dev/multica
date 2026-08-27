package handler

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// projectAuthRepository is the only Handler-side adapter for projectauth.
// Keeping SQL here means the permission package remains independent of sqlc
// generated code and upstream handler structure.
type projectAuthRepository struct{ db dbExecutor }

func newProjectAuthRepository(db dbExecutor) projectauth.Repository {
	if db == nil {
		return nil
	}
	return &projectAuthRepository{db: db}
}

func (r *projectAuthRepository) WorkspaceRole(ctx context.Context, workspaceID, userID string) (projectauth.WorkspaceRole, error) {
	var role string
	err := r.db.QueryRow(ctx, `SELECT role FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID).Scan(&role)
	return projectauth.WorkspaceRole(role), err
}

func (r *projectAuthRepository) ProjectRole(ctx context.Context, projectID, userID string) (projectauth.ProjectRole, error) {
	var role string
	err := r.db.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID).Scan(&role)
	return projectauth.ProjectRole(role), err
}

func (r *projectAuthRepository) ProjectWorkspace(ctx context.Context, projectID string) (string, error) {
	var workspaceID string
	err := r.db.QueryRow(ctx, `SELECT workspace_id::text FROM project WHERE id = $1`, projectID).Scan(&workspaceID)
	return workspaceID, err
}

func (r *projectAuthRepository) VisibleProjectIDs(ctx context.Context, workspaceID, userID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT p.id::text
		FROM project p
		WHERE p.workspace_id = $1
		  AND (EXISTS (
				SELECT 1 FROM member m
				WHERE m.workspace_id = p.workspace_id AND m.user_id = $2
				AND m.role = 'owner'
			) OR EXISTS (
				SELECT 1 FROM project_members pm
				WHERE pm.project_id = p.id AND pm.user_id = $2
			))
		ORDER BY p.created_at DESC`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *projectAuthRepository) AddProjectMember(ctx context.Context, projectID, userID string, role projectauth.ProjectRole) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role)
		SELECT $1, $2, $3
		WHERE EXISTS (
			SELECT 1 FROM project p
			JOIN member m ON m.workspace_id = p.workspace_id
			WHERE p.id = $1 AND m.user_id = $2
		)
		ON CONFLICT (project_id, user_id)
		DO UPDATE SET role = EXCLUDED.role, updated_at = now()`, projectID, userID, role)
	return err
}

// 2026-08-27 coder(lq): Keep automatic role upgrades atomic in PostgreSQL so
// concurrent assignment and mention events cannot downgrade an existing role.
func (r *projectAuthRepository) PromoteProjectMember(ctx context.Context, projectID, userID string, minimumRole projectauth.ProjectRole) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role)
		SELECT $1, $2, $3
		WHERE EXISTS (
			SELECT 1 FROM project p
			JOIN member m ON m.workspace_id = p.workspace_id
			WHERE p.id = $1 AND m.user_id = $2
		)
		ON CONFLICT (project_id, user_id)
		DO UPDATE SET role = CASE
			WHEN CASE project_members.role
				WHEN 'owner' THEN 4 WHEN 'manager' THEN 3 WHEN 'member' THEN 2 ELSE 1 END
				>= CASE EXCLUDED.role
				WHEN 'owner' THEN 4 WHEN 'manager' THEN 3 WHEN 'member' THEN 2 ELSE 1 END
			THEN project_members.role
			ELSE EXCLUDED.role
		END, updated_at = now()`, projectID, userID, minimumRole)
	return err
}

func (r *projectAuthRepository) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	return err
}

func (r *projectAuthRepository) ListProjectMembers(ctx context.Context, projectID string) ([]projectauth.ProjectMemberRecord, error) {
	rows, err := r.db.Query(ctx, `
		SELECT project_id::text, user_id::text, role
		FROM project_members WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []projectauth.ProjectMemberRecord
	for rows.Next() {
		var member projectauth.ProjectMemberRecord
		var role string
		if err := rows.Scan(&member.ProjectID, &member.UserID, &role); err != nil {
			return nil, err
		}
		member.Role = projectauth.ProjectRole(role)
		result = append(result, member)
	}
	return result, rows.Err()
}

// 2026-08-24 coder(lq): Keep report SQL in the Handler adapter so the
// projectauth package does not depend on sqlc or the upstream schema layer.
// Each UNION branch preserves the authorization source for audit/reporting.
func (r *projectAuthRepository) ListPermissionReport(ctx context.Context, filter projectauth.PermissionReportFilter) (projectauth.PermissionReportResult, error) {
	rows, err := r.db.Query(ctx, `
		WITH permission_map(project_role, permission) AS (
			VALUES
				('owner', 'project.view'), ('owner', 'project.edit'),
				('owner', 'project.issue.create'), ('owner', 'project.issue.manage'),
				('owner', 'project.agent.use'), ('owner', 'project.member.manage'),
				('owner', 'project.settings.manage'),
				('manager', 'project.view'), ('manager', 'project.edit'),
				('manager', 'project.issue.create'), ('manager', 'project.issue.manage'),
				('manager', 'project.agent.use'),
				('member', 'project.view'), ('member', 'project.issue.create'),
				('member', 'project.agent.use'),
				('viewer', 'project.view')
		), report_rows AS (
			SELECT 'project'::text AS scope, p.id::text AS project_id, p.title AS project_title,
				m.user_id::text AS user_id, u.name AS user_name, u.email AS user_email,
				m.role AS workspace_role, NULL::text AS project_role,
				pm.permission AS permission, 'workspace_role'::text AS source,
				NULL::text AS granted_by
			FROM project p
			JOIN member m ON m.workspace_id = p.workspace_id
			JOIN "user" u ON u.id = m.user_id
			CROSS JOIN (VALUES ('project.view'), ('project.edit'), ('project.issue.create'),
				('project.issue.manage'), ('project.agent.use'), ('project.member.manage'),
				('project.settings.manage')) AS pm(permission)
			WHERE m.role = 'owner'

			UNION ALL

			SELECT 'project', p.id::text, p.title,
				pm.user_id::text, u.name, u.email, m.role, pm.role,
				permission_map.permission, 'project_role', NULL
			FROM project_members pm
			JOIN project p ON p.id = pm.project_id
			JOIN "user" u ON u.id = pm.user_id
			LEFT JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id = pm.user_id
			JOIN permission_map ON permission_map.project_role = pm.role

		)
		SELECT scope, project_id, project_title,
			user_id, user_name, user_email, workspace_role, project_role,
			permission, source, granted_by, COUNT(*) OVER() AS total_count
		FROM report_rows
		WHERE project_id IN (SELECT id::text FROM project WHERE workspace_id = $1)
		  AND ($2 = '' OR project_id = $2)
		  AND ($3 = '' OR user_id = $3)
		  AND ($4 = '' OR workspace_role = $4 OR project_role = $4)
		  AND ($5 = '' OR permission = $5)
		  AND ($6 = 'all' OR scope = $6)
		ORDER BY project_title, project_id, user_name, permission, source
		LIMIT $7 OFFSET $8`,
		filter.WorkspaceID, filter.ProjectID, filter.UserID,
		filter.Role, string(filter.Permission), filter.Scope, filter.Limit, filter.Offset)
	if err != nil {
		return projectauth.PermissionReportResult{}, err
	}
	defer rows.Close()

	result := projectauth.PermissionReportResult{Rows: make([]projectauth.PermissionReportRow, 0)}
	for rows.Next() {
		var row projectauth.PermissionReportRow
		var projectRole, grantedBy pgtype.Text
		var workspaceRole, permission, source pgtype.Text
		var total int64
		if err := rows.Scan(&row.Scope, &row.ProjectID, &row.ProjectTitle,
			&row.UserID, &row.UserName, &row.UserEmail, &workspaceRole, &projectRole,
			&permission, &source, &grantedBy, &total); err != nil {
			return projectauth.PermissionReportResult{}, err
		}
		if workspaceRole.Valid {
			row.WorkspaceRole = projectauth.WorkspaceRole(workspaceRole.String)
		}
		if permission.Valid {
			row.Permission = projectauth.Permission(permission.String)
		}
		if source.Valid {
			row.Source = source.String
		}
		if projectRole.Valid {
			row.ProjectRole = projectauth.ProjectRole(projectRole.String)
		}
		if grantedBy.Valid {
			row.GrantedBy = grantedBy.String
		}
		result.Rows = append(result.Rows, row)
		result.Total = total
	}
	return result, rows.Err()
}
