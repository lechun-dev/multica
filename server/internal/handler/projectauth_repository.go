package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// projectAuthRepository is the only Handler-side adapter for projectauth.
// Keeping SQL here means the permission package remains independent of sqlc
// generated code and upstream handler structure.
type projectAuthRepository struct{ db dbExecutor }

var projectPermissionValues = []string{"project.view", "project.edit", "project.issue.create", "project.issue.manage", "project.agent.use", "project.member.manage", "project.settings.manage"}

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

func (r *projectAuthRepository) RolePermissions(ctx context.Context, workspaceID string, role projectauth.ProjectRole) ([]projectauth.Permission, bool, error) {
	permissions, found, err := r.queryRolePermissions(ctx, workspaceID, role)
	if err != nil {
		// 2026-08-28 coder(lq): Keep old deployments usable until migration 439
		// has been applied; the policy layer will use its compatibility defaults
		// when the overlay tables do not exist yet.
		if projectPermissionSchemaMissing(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if found || !projectauth.IsSystemRole(role) {
		return permissions, found, nil
	}
	// 2026-08-28 coder(lq): System roles are persisted workspace records. Seed
	// a role set for workspaces created after migration 439 before resolving
	// their permissions, while preserving an explicitly empty permission set.
	if err := r.ensureSystemRoleDefinitions(ctx, workspaceID); err != nil {
		if projectPermissionSchemaMissing(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return r.queryRolePermissions(ctx, workspaceID, role)
}

func (r *projectAuthRepository) queryRolePermissions(ctx context.Context, workspaceID string, role projectauth.ProjectRole) ([]projectauth.Permission, bool, error) {
	rows, err := r.db.Query(ctx, `
		SELECT p.permission
		FROM project_permission_roles role
		LEFT JOIN project_permission_role_permissions p ON p.role_id = role.id
		WHERE role.workspace_id = $1 AND role.role_key = $2
		ORDER BY p.permission`, workspaceID, string(role))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	permissions := make([]projectauth.Permission, 0)
	found := false
	for rows.Next() {
		var permission *string
		if err := rows.Scan(&permission); err != nil {
			return nil, false, err
		}
		found = true
		if permission != nil {
			permissions = append(permissions, projectauth.Permission(*permission))
		}
	}
	return permissions, found, rows.Err()
}

func projectPermissionSchemaMissing(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func (r *projectAuthRepository) ListRoleDefinitions(ctx context.Context, workspaceID string) ([]projectauth.RoleDefinition, error) {
	if err := r.ensureSystemRoleDefinitions(ctx, workspaceID); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT role.id::text, role.workspace_id::text, role.role_key, role.name,
		       role.description, role.is_system, COALESCE(array_agg(p.permission ORDER BY p.permission) FILTER (WHERE p.permission IS NOT NULL), '{}')
		FROM project_permission_roles role
		LEFT JOIN project_permission_role_permissions p ON p.role_id = role.id
		WHERE role.workspace_id = $1
		GROUP BY role.id ORDER BY role.is_system DESC, role.name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]projectauth.RoleDefinition, 0)
	for rows.Next() {
		var role projectauth.RoleDefinition
		var permissions []string
		if err := rows.Scan(&role.ID, &role.WorkspaceID, &role.Key, &role.Name, &role.Description, &role.IsSystem, &permissions); err != nil {
			return nil, err
		}
		for _, permission := range permissions {
			role.Permissions = append(role.Permissions, projectauth.Permission(permission))
		}
		result = append(result, role)
	}
	return result, rows.Err()
}

func (r *projectAuthRepository) GetRoleDefinition(ctx context.Context, workspaceID, key string) (projectauth.RoleDefinition, error) {
	roles, err := r.ListRoleDefinitions(ctx, workspaceID)
	if err != nil {
		return projectauth.RoleDefinition{}, err
	}
	for _, role := range roles {
		if string(role.Key) == key {
			return role, nil
		}
	}
	return projectauth.RoleDefinition{}, fmt.Errorf("role %q not found", key)
}

func (r *projectAuthRepository) CreateRoleDefinition(ctx context.Context, workspaceID, createdBy string, role projectauth.RoleDefinition) (projectauth.RoleDefinition, error) {
	if _, err := r.db.Exec(ctx, `INSERT INTO project_permission_roles (workspace_id, role_key, name, description, is_system, created_by) VALUES ($1,$2,$3,$4,false,$5)`, workspaceID, string(role.Key), role.Name, role.Description, createdBy); err != nil {
		return projectauth.RoleDefinition{}, err
	}
	if err := r.replaceRolePermissions(ctx, workspaceID, role.Key, role.Permissions); err != nil {
		return projectauth.RoleDefinition{}, err
	}
	return r.GetRoleDefinition(ctx, workspaceID, string(role.Key))
}

func (r *projectAuthRepository) UpdateRoleDefinition(ctx context.Context, workspaceID, key string, role projectauth.RoleDefinition) (projectauth.RoleDefinition, error) {
	if _, err := r.db.Exec(ctx, `UPDATE project_permission_roles SET name=$3, description=$4, updated_at=now() WHERE workspace_id=$1 AND role_key=$2`, workspaceID, key, role.Name, role.Description); err != nil {
		return projectauth.RoleDefinition{}, err
	}
	if err := r.replaceRolePermissions(ctx, workspaceID, projectauth.ProjectRole(key), role.Permissions); err != nil {
		return projectauth.RoleDefinition{}, err
	}
	return r.GetRoleDefinition(ctx, workspaceID, key)
}

func (r *projectAuthRepository) DeleteRoleDefinition(ctx context.Context, workspaceID, key string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM project_permission_roles WHERE workspace_id=$1 AND role_key=$2 AND is_system=false AND NOT EXISTS (SELECT 1 FROM project_members pm WHERE pm.custom_role_id = project_permission_roles.id OR (pm.custom_role_id IS NULL AND pm.role = project_permission_roles.role_key))`, workspaceID, key)
	if err == nil && tag.RowsAffected() == 0 {
		return projectauth.ErrRoleInUse
	}
	return err
}

func (r *projectAuthRepository) ensureSystemRoleDefinitions(ctx context.Context, workspaceID string) error {
	_, err := r.db.Exec(ctx, `
		WITH system_roles(role_key, name) AS (
			VALUES ('owner', 'Owner'), ('manager', 'Manager'), ('member', 'Member'), ('viewer', 'Viewer')
		)
		INSERT INTO project_permission_roles (workspace_id, role_key, name, is_system)
		SELECT $1, role_key, name, true FROM system_roles
		ON CONFLICT (workspace_id, role_key) DO NOTHING`, workspaceID)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO project_permission_role_permissions (role_id, permission)
		SELECT role_def.id, defaults.permission
		FROM project_permission_roles role_def
		JOIN (VALUES
			('owner','project.view'), ('owner','project.edit'), ('owner','project.issue.create'),
			('owner','project.issue.manage'), ('owner','project.agent.use'), ('owner','project.member.manage'),
			('owner','project.settings.manage'), ('manager','project.view'), ('manager','project.edit'),
			('manager','project.issue.create'), ('manager','project.issue.manage'), ('manager','project.agent.use'),
			('member','project.view'), ('member','project.issue.create'), ('member','project.agent.use'),
			('viewer','project.view')
		) AS defaults(role_key, permission) ON defaults.role_key = role_def.role_key
		WHERE role_def.workspace_id = $1 AND role_def.is_system
		ON CONFLICT DO NOTHING`, workspaceID)
	return err
}

func (r *projectAuthRepository) replaceRolePermissions(ctx context.Context, workspaceID string, key projectauth.ProjectRole, permissions []projectauth.Permission) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM project_permission_role_permissions WHERE role_id = (SELECT id FROM project_permission_roles WHERE workspace_id=$1 AND role_key=$2)`, workspaceID, string(key)); err != nil {
		return err
	}
	for _, permission := range permissions {
		if !containsProjectPermission(projectPermissionValues, string(permission)) {
			return fmt.Errorf("invalid project permission %q", permission)
		}
		if _, err := r.db.Exec(ctx, `INSERT INTO project_permission_role_permissions (role_id, permission) SELECT id, $3 FROM project_permission_roles WHERE workspace_id=$1 AND role_key=$2`, workspaceID, string(key), string(permission)); err != nil {
			return err
		}
	}
	return nil
}

func containsProjectPermission(values []string, value string) bool {
	return strings.TrimSpace(value) != "" && func() bool {
		for _, candidate := range values {
			if candidate == value {
				return true
			}
		}
		return false
	}()
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
		INSERT INTO project_members (project_id, user_id, role, custom_role_id)
		SELECT $1, $2, $3
			, (SELECT id FROM project_permission_roles WHERE workspace_id = (SELECT workspace_id FROM project WHERE id=$1) AND role_key=$3 AND is_system=false)
		WHERE EXISTS (
			SELECT 1 FROM project p
			JOIN member m ON m.workspace_id = p.workspace_id
			WHERE p.id = $1 AND m.user_id = $2
		)
		ON CONFLICT (project_id, user_id)
		DO UPDATE SET role = EXCLUDED.role, custom_role_id = EXCLUDED.custom_role_id, updated_at = now()`, projectID, userID, role)
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
			WHEN project_members.custom_role_id IS NOT NULL THEN project_members.role
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
		WITH report_rows AS (
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
				role_permission.permission, 'project_role', NULL
			FROM project_members pm
			JOIN project p ON p.id = pm.project_id
			JOIN "user" u ON u.id = pm.user_id
			LEFT JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id = pm.user_id
			JOIN project_permission_roles role_def
			  ON role_def.workspace_id = p.workspace_id AND role_def.role_key = pm.role
			JOIN project_permission_role_permissions role_permission ON role_permission.role_id = role_def.id

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
