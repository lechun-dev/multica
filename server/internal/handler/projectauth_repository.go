package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// projectAuthRepository is the only Handler-side adapter for projectauth.
// Keeping SQL here means the permission package remains independent of sqlc
// generated code and upstream handler structure.
type projectAuthRepository struct{ db dbExecutor }

var projectPermissionValues = []string{"project.view", "project.edit", "project.issue.create", "project.issue.comment", "project.issue.manage", "project.agent.use", "project.member.manage", "project.settings.manage"}

func (r *projectAuthRepository) RecordAuthorizationAudit(ctx context.Context, event projectauth.AuthorizationAuditEvent) error {
	details, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details)
		VALUES ($1, NULLIF($2, '')::uuid, 'member', NULLIF($3, '')::uuid, $4, $5::jsonb)`,
		event.WorkspaceID, event.IssueID, event.ActorUserID, event.Action, details)
	return err
}

func newProjectAuthRepository(db dbExecutor) projectauth.Repository {
	if db == nil {
		return nil
	}
	return &projectAuthRepository{db: db}
}

func (r *projectAuthRepository) WorkspaceRole(ctx context.Context, workspaceID, userID string) (projectauth.WorkspaceRole, error) {
	var role string
	err := r.db.QueryRow(ctx, `SELECT role FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", projectauth.ErrNotWorkspaceMember
	}
	return projectauth.WorkspaceRole(role), wrapProjectPermissionRepositoryError(err)
}

// UserInWorkspace and ActiveOrganizationInWorkspace are deliberately narrow
// directory queries. They let the provider-neutral authorization service
// reject stale/cross-workspace subjects without coupling it to an OA API.
// 2026-09-01 coder(lq): Add subject existence checks at the persistence seam.
func (r *projectAuthRepository) UserInWorkspace(ctx context.Context, workspaceID, userID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2)`, workspaceID, userID).Scan(&exists)
	return exists, wrapProjectPermissionRepositoryError(err)
}

func (r *projectAuthRepository) ActiveOrganizationInWorkspace(ctx context.Context, workspaceID, organizationID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projectauth_organizations WHERE workspace_id=$1 AND id=$2::uuid AND status='active')`, workspaceID, organizationID).Scan(&exists)
	if err != nil && projectPermissionSchemaMissing(err) {
		return false, wrapProjectPermissionRepositoryError(err)
	}
	return exists, err
}

// WorkspaceOwnerBypassEnabled reads the deployment-level project permission
// switch from the process environment. Workspace settings no longer own this
// decision, which keeps the toggle out of the editable page state.
func (r *projectAuthRepository) WorkspaceOwnerBypassEnabled(ctx context.Context, workspaceID string) (bool, error) {
	_ = ctx
	_ = workspaceID
	return os.Getenv("PROJECT_OWNER_BYPASS_ENABLED") != "false", nil
}

func (r *projectAuthRepository) ProjectRole(ctx context.Context, projectID, userID string) (projectauth.ProjectRole, error) {
	var role string
	err := r.db.QueryRow(ctx, `
		SELECT role_key FROM projectauth_access_grants
		WHERE project_id=$1 AND issue_id IS NULL AND role_key IS NOT NULL
		  AND ((subject_type='user' AND subject_id=$2)
		    OR (subject_type='everyone' AND (subject_id='' OR subject_id=(SELECT workspace_id::text FROM project WHERE id=$1)))
		    OR (subject_type='organization' AND subject_id IN (
		        SELECT om.organization_id::text
		        FROM projectauth_organization_members om
		        JOIN projectauth_organizations org ON org.id = om.organization_id
		        WHERE om.user_id=$2 AND om.workspace_id=(SELECT workspace_id FROM project WHERE id=$1)
		          AND org.status = 'active'
		    )))
		ORDER BY CASE role_key WHEN 'owner' THEN 4 WHEN 'manager' THEN 3 WHEN 'member' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END DESC
		LIMIT 1`, projectID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", projectauth.ErrNoProjectAccess
	}
	return projectauth.ProjectRole(role), wrapProjectPermissionRepositoryError(err)
}

func (r *projectAuthRepository) IssuePermission(ctx context.Context, issueID, userID string, permission projectauth.Permission) (bool, error) {
	// 2026-09-01 coder(lq): Keep this compatibility method on the canonical
	// grants table. New authorization checks use projectauth.Service.CheckIssue;
	// this narrow reader exists only for older callers and must never make the
	// legacy issue_permissions table an authorization source.
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM issue i
			JOIN projectauth_access_grants g
			  ON g.workspace_id = i.workspace_id
			 AND g.project_id = i.project_id
			 AND g.issue_id = i.id
			 AND g.subject_type = 'user'
			 AND g.subject_id = $2
			 AND g.permission = $3
			WHERE i.id = $1 AND i.project_id IS NOT NULL
		)`, issueID, userID, string(permission)).Scan(&exists)
	return exists, wrapProjectPermissionRepositoryError(err)
}

// 2026-08-31 coder(lq): Unified grant reads are kept in this adapter so the
// projectauth package remains independent of PostgreSQL and generated models.
func (r *projectAuthRepository) ListAccessGrants(ctx context.Context, workspaceID, projectID, issueID string) ([]projectauth.AccessGrant, error) {
	query := `
		SELECT id::text, workspace_id::text, project_id::text, COALESCE(issue_id::text, ''),
		       subject_type, COALESCE(subject_id, ''), COALESCE(role_key, ''),
		       COALESCE(permission, ''), source, COALESCE(granted_by::text, '')
		FROM projectauth_access_grants
		WHERE workspace_id = $1 AND project_id = $2 AND (($3 = '' AND issue_id IS NULL) OR ($3 <> '' AND (issue_id IS NULL OR issue_id = $3::uuid)))
		ORDER BY created_at, id`
	rows, err := r.db.Query(ctx, query, workspaceID, projectID, issueID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	grants := make([]projectauth.AccessGrant, 0)
	for rows.Next() {
		var grant projectauth.AccessGrant
		var issueID, roleKey, permission, grantedBy string
		if err := rows.Scan(&grant.ID, &grant.WorkspaceID, &grant.ProjectID, &issueID,
			&grant.SubjectType, &grant.SubjectID, &roleKey, &permission, &grant.Source, &grantedBy); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		grant.IssueID, grant.Role, grant.Permission, grant.GrantedBy = issueID, projectauth.ProjectRole(roleKey), projectauth.Permission(permission), grantedBy
		grants = append(grants, grant)
	}
	return grants, wrapProjectPermissionRepositoryError(rows.Err())
}

// GetAccessGrant reads the canonical row after an upsert so callers receive
// generated IDs and normalized source/actor fields in the POST response.
// 2026-08-31 coder(lq): Keep read-after-write in the PostgreSQL adapter; the
// projectauth package remains storage-neutral.
func (r *projectAuthRepository) GetAccessGrant(ctx context.Context, workspaceID, projectID, issueID string,
	subjectType projectauth.SubjectType, subjectID string, role projectauth.ProjectRole, permission projectauth.Permission) (projectauth.AccessGrant, error) {
	var grant projectauth.AccessGrant
	var issue, roleKey, permissionKey, source, grantedBy string
	err := r.db.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, project_id::text, COALESCE(issue_id::text, ''),
		       subject_type, COALESCE(subject_id, ''), COALESCE(role_key, ''),
		       COALESCE(permission, ''), source, COALESCE(granted_by::text, '')
		FROM projectauth_access_grants
		WHERE workspace_id=$1 AND project_id=$2
		  AND issue_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid
		  AND subject_type=$4 AND COALESCE(subject_id, '') = COALESCE($5, '')
		  AND role_key IS NOT DISTINCT FROM NULLIF($6,'')
		  AND permission IS NOT DISTINCT FROM NULLIF($7,'')
		LIMIT 1`, workspaceID, projectID, issueID, string(subjectType), subjectID, string(role), string(permission)).
		Scan(&grant.ID, &grant.WorkspaceID, &grant.ProjectID, &issue, &grant.SubjectType, &grant.SubjectID,
			&roleKey, &permissionKey, &source, &grantedBy)
	if err != nil {
		return projectauth.AccessGrant{}, wrapProjectPermissionRepositoryError(err)
	}
	grant.IssueID = issue
	grant.Role = projectauth.ProjectRole(roleKey)
	grant.Permission = projectauth.Permission(permissionKey)
	grant.Source = projectauth.GrantSource(source)
	grant.GrantedBy = grantedBy
	return grant, nil
}

func (r *projectAuthRepository) ListUserOrganizations(ctx context.Context, workspaceID, userID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT organization_id::text
		FROM projectauth_organization_members om
		JOIN projectauth_organizations org ON org.id = om.organization_id
		WHERE om.workspace_id = $1 AND om.user_id = $2 AND org.status = 'active'`, workspaceID, userID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	organizations := make([]string, 0)
	for rows.Next() {
		var organizationID string
		if err := rows.Scan(&organizationID); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		organizations = append(organizations, organizationID)
	}
	return organizations, wrapProjectPermissionRepositoryError(rows.Err())
}

// ListOrganizations reads only the provider-neutral directory snapshot. The
// sync workers own provider API calls; HTTP authorization requests stay local
// and deterministic. 2026-09-01 coder(lq): Add organization picker query.
func (r *projectAuthRepository) ListOrganizations(ctx context.Context, workspaceID string) ([]projectauth.Organization, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, workspace_id::text, provider, external_id,
		       name, COALESCE(parent_id::text, ''), status
		FROM projectauth_organizations
		WHERE workspace_id = $1 AND status = 'active'
		ORDER BY name, provider, external_id`, workspaceID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	organizations := make([]projectauth.Organization, 0)
	for rows.Next() {
		var organization projectauth.Organization
		if err := rows.Scan(&organization.ID, &organization.WorkspaceID, &organization.Provider,
			&organization.ExternalID, &organization.Name, &organization.ParentID, &organization.Status); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		organizations = append(organizations, organization)
	}
	return organizations, wrapProjectPermissionRepositoryError(rows.Err())
}

func (r *projectAuthRepository) UpsertAccessGrant(ctx context.Context, grant projectauth.AccessGrant) error {
	if grant.WorkspaceID == "" && grant.ProjectID != "" {
		workspaceID, err := r.ProjectWorkspace(ctx, grant.ProjectID)
		if err != nil {
			return err
		}
		grant.WorkspaceID = workspaceID
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO projectauth_access_grants
			(workspace_id, project_id, issue_id, subject_type, subject_id, role_key, permission, source, granted_by)
		VALUES ($1,$2,NULLIF($3,'')::uuid,$4,COALESCE($5,''),NULLIF($6,''),NULLIF($7,''),$8,NULLIF($9,'')::uuid)
		ON CONFLICT DO UPDATE
		DO UPDATE SET source = EXCLUDED.source, granted_by = EXCLUDED.granted_by, updated_at = now()`,
		grant.WorkspaceID, grant.ProjectID, grant.IssueID, string(grant.SubjectType), grant.SubjectID,
		string(grant.Role), string(grant.Permission), string(grant.Source), grant.GrantedBy)
	return wrapProjectPermissionRepositoryError(err)
}

func (r *projectAuthRepository) DeleteAccessGrant(ctx context.Context, workspaceID, projectID, issueID string, subjectType projectauth.SubjectType, subjectID string, role projectauth.ProjectRole, permission projectauth.Permission) error {
	_, err := r.db.Exec(ctx, `
		DELETE FROM projectauth_access_grants
		WHERE workspace_id=$1 AND project_id=$2 AND issue_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid
		  AND subject_type=$4 AND COALESCE(subject_id, '') = COALESCE($5, '')
		  AND role_key IS NOT DISTINCT FROM NULLIF($6,'')
		  AND permission IS NOT DISTINCT FROM NULLIF($7,'')`, workspaceID, projectID, issueID,
		string(subjectType), subjectID, string(role), string(permission))
	return wrapProjectPermissionRepositoryError(err)
}

// CurrentProjectRoles resolves explicit project grants in one query for the
// project list response. Workspace-owner inheritance remains an access-control
// rule, but it is intentionally not reported as a project role: the table's
// "my role" column must describe the caller's membership on that project.
// 2026-08-31 coder(lq): Keep effective workspace access separate from project
// membership metadata so workspace owners do not appear as project owners.
func (r *projectAuthRepository) CurrentProjectRoles(ctx context.Context, workspaceID, userID string) (map[string]projectauth.ProjectRole, error) {
	rows, err := r.db.Query(ctx, `
		SELECT project_id::text, role_key
		FROM projectauth_access_grants g
		WHERE g.workspace_id=$1 AND g.issue_id IS NULL AND g.role_key IS NOT NULL
		  AND (
			(g.subject_type='user' AND g.subject_id=$2)
			OR (g.subject_type='everyone' AND (g.subject_id='' OR g.subject_id=$1))
			OR (g.subject_type='organization' AND g.subject_id IN (
				SELECT organization_id::text
				FROM projectauth_organization_members
				WHERE workspace_id=$1 AND user_id=$2
				  AND organization_id IN (SELECT id FROM projectauth_organizations WHERE workspace_id=$1 AND status='active')
			))
		  )
		ORDER BY 1`, workspaceID, userID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	roles := make(map[string]projectauth.ProjectRole)
	for rows.Next() {
		var projectID string
		var role string
		if err := rows.Scan(&projectID, &role); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		if role != "" {
			candidate := projectauth.ProjectRole(role)
			if current, exists := roles[projectID]; !exists || projectRoleRank(candidate) > projectRoleRank(current) {
				roles[projectID] = candidate
			}
		}
	}
	return roles, wrapProjectPermissionRepositoryError(rows.Err())
}

// 2026-08-31 coder(lq): A user can receive different project roles through a
// direct, organization, everyone, or legacy grant. The list column reports the
// strongest explicit role deterministically; permission-only grants remain
// role-less instead of being presented as a misleading Owner role.
func projectRoleRank(role projectauth.ProjectRole) int {
	switch role {
	case projectauth.ProjectOwner:
		return 4
	case projectauth.ProjectManager:
		return 3
	case projectauth.ProjectMember:
		return 2
	case projectauth.ProjectViewer:
		return 1
	default:
		return 0
	}
}

func (r *projectAuthRepository) RolePermissions(ctx context.Context, workspaceID string, role projectauth.ProjectRole) ([]projectauth.Permission, bool, error) {
	permissions, found, err := r.queryRolePermissions(ctx, workspaceID, role)
	if err != nil {
		return nil, false, wrapProjectPermissionRepositoryError(err)
	}
	if found || !projectauth.IsSystemRole(role) {
		return permissions, found, nil
	}
	// 2026-08-28 coder(lq): System roles are persisted workspace records. Seed
	// a role set for workspaces created after migration 439 before resolving
	// their permissions, while preserving an explicitly empty permission set.
	if err := r.ensureSystemRoleDefinitions(ctx, workspaceID); err != nil {
		return nil, false, wrapProjectPermissionRepositoryError(err)
	}
	permissions, found, err = r.queryRolePermissions(ctx, workspaceID, role)
	return permissions, found, wrapProjectPermissionRepositoryError(err)
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
	return permissions, found, wrapProjectPermissionRepositoryError(rows.Err())
}

func projectPermissionSchemaMissing(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	// 2026-08-28 coder(lq): Treat missing tables and columns as the same
	// migration-state problem. A partially applied 439 can create one overlay
	// table while leaving a required column absent, which otherwise surfaces as
	// the generic report error and gives self-hosted operators no next step.
	switch pgErr.Code {
	case "42P01", // undefined_table
		"42703": // undefined_column
		return true
	default:
		return false
	}
}

// 2026-09-01 coder(lq): Once the project-permission feature is enabled, the
// unified ACL schema is authoritative. Missing overlay tables/columns must be
// surfaced as a migration failure instead of silently falling back to legacy
// project_members or issue_permissions data.
func wrapProjectPermissionRepositoryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, projectauth.ErrMigrationRequired) || projectPermissionSchemaMissing(err) {
		return fmt.Errorf("%w: %v", projectauth.ErrMigrationRequired, err)
	}
	return err
}

func (r *projectAuthRepository) ListRoleDefinitions(ctx context.Context, workspaceID string) ([]projectauth.RoleDefinition, error) {
	if err := r.ensureSystemRoleDefinitions(ctx, workspaceID); err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	rows, err := r.db.Query(ctx, `
		SELECT role.id::text, role.workspace_id::text, role.role_key, role.name,
		       role.description, role.is_system, COALESCE(array_agg(p.permission ORDER BY p.permission) FILTER (WHERE p.permission IS NOT NULL), '{}')
		FROM project_permission_roles role
		LEFT JOIN project_permission_role_permissions p ON p.role_id = role.id
		WHERE role.workspace_id = $1
		GROUP BY role.id ORDER BY role.is_system DESC, role.name`, workspaceID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	result := make([]projectauth.RoleDefinition, 0)
	for rows.Next() {
		var role projectauth.RoleDefinition
		var permissions []string
		if err := rows.Scan(&role.ID, &role.WorkspaceID, &role.Key, &role.Name, &role.Description, &role.IsSystem, &permissions); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		for _, permission := range permissions {
			role.Permissions = append(role.Permissions, projectauth.Permission(permission))
		}
		result = append(result, role)
	}
	return result, wrapProjectPermissionRepositoryError(rows.Err())
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
		return projectauth.RoleDefinition{}, wrapProjectPermissionRepositoryError(err)
	}
	if err := r.replaceRolePermissions(ctx, workspaceID, role.Key, role.Permissions); err != nil {
		return projectauth.RoleDefinition{}, err
	}
	return r.GetRoleDefinition(ctx, workspaceID, string(role.Key))
}

func (r *projectAuthRepository) UpdateRoleDefinition(ctx context.Context, workspaceID, key string, role projectauth.RoleDefinition) (projectauth.RoleDefinition, error) {
	if _, err := r.db.Exec(ctx, `UPDATE project_permission_roles SET name=$3, description=$4, updated_at=now() WHERE workspace_id=$1 AND role_key=$2`, workspaceID, key, role.Name, role.Description); err != nil {
		return projectauth.RoleDefinition{}, wrapProjectPermissionRepositoryError(err)
	}
	if err := r.replaceRolePermissions(ctx, workspaceID, projectauth.ProjectRole(key), role.Permissions); err != nil {
		return projectauth.RoleDefinition{}, err
	}
	return r.GetRoleDefinition(ctx, workspaceID, key)
}

func (r *projectAuthRepository) DeleteRoleDefinition(ctx context.Context, workspaceID, key string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM project_permission_roles WHERE workspace_id=$1 AND role_key=$2 AND is_system=false
		AND NOT EXISTS (
			SELECT 1 FROM projectauth_access_grants g
			WHERE g.workspace_id=$1 AND g.role_key = project_permission_roles.role_key
		)`, workspaceID, key)
	if err != nil {
		return wrapProjectPermissionRepositoryError(err)
	}
	if tag.RowsAffected() == 0 {
		return projectauth.ErrRoleInUse
	}
	return nil
}

func (r *projectAuthRepository) ensureSystemRoleDefinitions(ctx context.Context, workspaceID string) error {
	// 2026-09-01 coder(lq): Seed defaults only for roles created by this call.
	// Existing rows are administrator-owned configuration; filling their
	// missing permissions would silently undo intentional permission removals.
	_, err := r.db.Exec(ctx, `
		WITH system_roles(role_key, name) AS (
			VALUES ('owner', 'Owner'), ('manager', 'Manager'), ('member', 'Member'), ('viewer', 'Viewer')
		), inserted_roles AS (
			INSERT INTO project_permission_roles (workspace_id, role_key, name, is_system)
			SELECT $1, role_key, name, true FROM system_roles
			ON CONFLICT (workspace_id, role_key) DO NOTHING
			RETURNING id, role_key
		)
		INSERT INTO project_permission_role_permissions (role_id, permission)
		SELECT inserted.id, defaults.permission
		FROM inserted_roles inserted
		JOIN (VALUES
			('owner','project.view'), ('owner','project.edit'), ('owner','project.issue.create'),
			('owner','project.issue.comment'), ('owner','project.issue.manage'), ('owner','project.agent.use'), ('owner','project.member.manage'),
			('owner','project.settings.manage'), ('manager','project.view'), ('manager','project.edit'),
			('manager','project.issue.create'), ('manager','project.issue.comment'), ('manager','project.issue.manage'), ('manager','project.agent.use'),
			('member','project.view'), ('member','project.issue.create'), ('member','project.issue.comment'), ('member','project.agent.use'),
			('viewer','project.view')
		) AS defaults(role_key, permission) ON defaults.role_key = inserted.role_key
		ON CONFLICT DO NOTHING`, workspaceID)
	return wrapProjectPermissionRepositoryError(err)
}

func (r *projectAuthRepository) replaceRolePermissions(ctx context.Context, workspaceID string, key projectauth.ProjectRole, permissions []projectauth.Permission) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM project_permission_role_permissions WHERE role_id = (SELECT id FROM project_permission_roles WHERE workspace_id=$1 AND role_key=$2)`, workspaceID, string(key)); err != nil {
		return wrapProjectPermissionRepositoryError(err)
	}
	for _, permission := range permissions {
		if !containsProjectPermission(projectPermissionValues, string(permission)) {
			return fmt.Errorf("invalid project permission %q", permission)
		}
		if _, err := r.db.Exec(ctx, `INSERT INTO project_permission_role_permissions (role_id, permission) SELECT id, $3 FROM project_permission_roles WHERE workspace_id=$1 AND role_key=$2`, workspaceID, string(key), string(permission)); err != nil {
			return wrapProjectPermissionRepositoryError(err)
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
	if errors.Is(err, pgx.ErrNoRows) {
		return "", projectauth.ErrNoProjectAccess
	}
	return workspaceID, wrapProjectPermissionRepositoryError(err)
}

// IssueProject resolves the canonical workspace/project binding for a task.
// 2026-08-31 coder(lq): Keep task grant writes and reads tied to the actual
// issue row so a caller cannot pair an issue UUID with another project.
func (r *projectAuthRepository) IssueProject(ctx context.Context, issueID string) (string, string, error) {
	var workspaceID, projectID string
	err := r.db.QueryRow(ctx, `
		SELECT workspace_id::text, project_id::text
		FROM issue
		WHERE id = $1 AND project_id IS NOT NULL`, issueID).Scan(&workspaceID, &projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", projectauth.ErrNoProjectAccess
	}
	return workspaceID, projectID, wrapProjectPermissionRepositoryError(err)
}

func (r *projectAuthRepository) VisibleProjectIDs(ctx context.Context, workspaceID, userID string) ([]string, error) {
	return r.VisibleProjectIDsWithWorkspaceScope(ctx, workspaceID, userID, true)
}

func (r *projectAuthRepository) VisibleProjectIDsWithWorkspaceScope(ctx context.Context, workspaceID, userID string, includeWorkspaceOwned bool) ([]string, error) {
	ownerClause := "FALSE"
	if includeWorkspaceOwned {
		ownerClause = fmt.Sprintf(`(%s) AND EXISTS (
				SELECT 1 FROM member m
				WHERE m.workspace_id = p.workspace_id AND m.user_id = $2
				AND m.role = 'owner'
			)`, workspaceOwnerBypassPredicate("p.workspace_id"))
	}
	visibilityClause := projectAccessPredicate("p.id", "$1", "$2")
	query := fmt.Sprintf(`
		SELECT p.id::text
		FROM project p
		WHERE p.workspace_id = $1
		  AND (%s OR %s)
		ORDER BY p.created_at DESC`, ownerClause, visibilityClause)
	rows, err := r.db.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		ids = append(ids, id)
	}
	return ids, wrapProjectPermissionRepositoryError(rows.Err())
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
	if err != nil {
		return err
	}
	return r.UpsertAccessGrant(ctx, projectauth.AccessGrant{ProjectID: projectID, SubjectType: projectauth.SubjectUser, SubjectID: userID, Role: role, Source: projectauth.GrantSourceManual})
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
	if err != nil {
		return err
	}
	return r.UpsertAccessGrant(ctx, projectauth.AccessGrant{ProjectID: projectID, SubjectType: projectauth.SubjectUser, SubjectID: userID, Role: minimumRole, Source: projectauth.GrantSourceSystem})
}

func (r *projectAuthRepository) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `DELETE FROM projectauth_access_grants WHERE project_id=$1 AND issue_id IS NULL AND subject_type='user' AND subject_id=$2`, projectID, userID)
	return err
}

func (r *projectAuthRepository) ListProjectMembers(ctx context.Context, projectID string) ([]projectauth.ProjectMemberRecord, error) {
	rows, err := r.db.Query(ctx, `
		-- 2026-09-01 coder(lq): The unified grant table is the sole source for
		-- project membership reads when the overlay is enabled. Keep this API's
		-- response shape for existing callers; organization/everyone grants are
		-- exposed through the access-grant API rather than fabricated as users.
		SELECT project_id::text, subject_id, role_key
		FROM projectauth_access_grants
		WHERE project_id = $1 AND issue_id IS NULL
		  AND subject_type = 'user' AND role_key IS NOT NULL
		ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()
	var result []projectauth.ProjectMemberRecord
	for rows.Next() {
		var member projectauth.ProjectMemberRecord
		var role string
		if err := rows.Scan(&member.ProjectID, &member.UserID, &role); err != nil {
			return nil, wrapProjectPermissionRepositoryError(err)
		}
		member.Role = projectauth.ProjectRole(role)
		result = append(result, member)
	}
	return result, wrapProjectPermissionRepositoryError(rows.Err())
}

// 2026-08-31 coder(lq): Keep report SQL in the Handler adapter so the
// projectauth package does not depend on sqlc or the upstream schema layer.
// The query starts from the unified grant fact table, expands organization and
// everyone subjects to effective users, and materializes project grants as
// inherited task rows. Legacy ACL tables are intentionally not read once the
// overlay is enabled; migration 453 is responsible for backfilling them.
func (r *projectAuthRepository) ListPermissionReport(ctx context.Context, filter projectauth.PermissionReportFilter) (projectauth.PermissionReportResult, error) {
	rows, err := r.db.Query(ctx, `
		WITH canonical AS (
			SELECT g.id::text AS grant_id, g.workspace_id::text AS workspace_id,
				g.project_id::text AS project_id, COALESCE(g.issue_id::text, '') AS issue_id,
				g.subject_type, COALESCE(g.subject_id, '') AS subject_id,
				g.role_key, g.permission, g.source, COALESCE(g.granted_by::text, '') AS granted_by
			FROM projectauth_access_grants g
			JOIN project p ON p.id = g.project_id
			WHERE g.workspace_id = $1
			  AND ($2 = '' OR g.project_id::text = $2)
			  -- Project grants remain in scope when the report is filtered to one
			  -- issue because they materialize inherited permissions below.
			  AND ($3 = '' OR g.issue_id IS NULL OR g.issue_id::text = $3)
		), permission_rows AS (
			SELECT c.*, c.permission AS permission_key
			FROM canonical c WHERE c.permission IS NOT NULL
			UNION ALL
			SELECT c.*, rp.permission AS permission_key
			FROM canonical c
			JOIN project_permission_roles rd
			  ON rd.workspace_id = c.workspace_id::uuid AND rd.role_key = c.role_key
			JOIN project_permission_role_permissions rp ON rp.role_id = rd.id
			WHERE c.role_key IS NOT NULL
		), role_members AS (
			-- A role subject targets users who hold that project role. Expand
			-- every project-level role grant source, including organization and
			-- everyone, and deduplicate users who match more than one path.
			SELECT DISTINCT c.project_id, c.subject_id AS user_id, c.role_key
			FROM canonical c
			WHERE c.issue_id = '' AND c.subject_type = 'user' AND c.role_key IS NOT NULL
			UNION
			SELECT DISTINCT c.project_id, om.user_id::text, c.role_key
			FROM canonical c
			JOIN projectauth_organizations org
			  ON org.workspace_id::text = c.workspace_id
			 AND org.id::text = c.subject_id
			 AND org.status = 'active'
			JOIN projectauth_organization_members om
			  ON om.organization_id = org.id
			 AND om.workspace_id::text = c.workspace_id
			WHERE c.issue_id = '' AND c.subject_type = 'organization' AND c.role_key IS NOT NULL
			UNION
			SELECT DISTINCT c.project_id, m.user_id::text, c.role_key
			FROM canonical c
			JOIN member m ON m.workspace_id::text = c.workspace_id
			WHERE c.issue_id = '' AND c.subject_type = 'everyone' AND c.role_key IS NOT NULL
		), subjects AS (
			SELECT pr.*, pr.subject_id AS effective_user_id
			FROM permission_rows pr WHERE pr.subject_type = 'user'
			UNION ALL
			SELECT pr.*, om.user_id::text
			FROM permission_rows pr
			JOIN projectauth_organizations org
			  ON org.workspace_id::text = pr.workspace_id AND org.id::text = pr.subject_id
			JOIN projectauth_organization_members om ON om.organization_id = org.id
			WHERE pr.subject_type = 'organization' AND org.status = 'active'
			UNION ALL
			SELECT pr.*, m.user_id::text
			FROM permission_rows pr
			JOIN member m ON m.workspace_id::text = pr.workspace_id
			WHERE pr.subject_type = 'everyone'
			UNION ALL
			SELECT pr.*, rm.user_id
			FROM permission_rows pr
			JOIN role_members rm ON rm.project_id = pr.project_id AND rm.role_key = pr.subject_id
			WHERE pr.subject_type = 'role'
		), report_rows AS (
			SELECT 'project'::text AS scope, s.project_id, p.title AS project_title,
				''::text AS issue_id, ''::text AS issue_title, s.effective_user_id AS user_id,
				COALESCE(u.name, '') AS user_name, COALESCE(u.email, '') AS user_email, COALESCE(m.role, '') AS workspace_role,
				COALESCE(s.role_key, '') AS project_role, s.permission_key AS permission,
				s.source, s.granted_by, s.subject_type, s.subject_id, FALSE AS inherited_from_project
			FROM subjects s
			JOIN project p ON p.id::text = s.project_id
			LEFT JOIN "user" u ON u.id::text = NULLIF(s.effective_user_id, '')
			LEFT JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id::text = NULLIF(s.effective_user_id, '')
			WHERE s.issue_id = ''

			UNION ALL

			SELECT 'issue', s.project_id, p.title, i.id::text, i.title, s.effective_user_id,
				COALESCE(u.name, ''), COALESCE(u.email, ''), COALESCE(m.role, ''), COALESCE(s.role_key, ''), s.permission_key,
				s.source, s.granted_by, s.subject_type, s.subject_id, FALSE
			FROM subjects s
			JOIN project p ON p.id::text = s.project_id
			JOIN issue i ON i.id::text = s.issue_id AND i.project_id = p.id
			LEFT JOIN "user" u ON u.id::text = NULLIF(s.effective_user_id, '')
			LEFT JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id::text = NULLIF(s.effective_user_id, '')
			WHERE s.issue_id <> ''

			UNION ALL

			SELECT 'issue', s.project_id, p.title, i.id::text, i.title, s.effective_user_id,
				COALESCE(u.name, ''), COALESCE(u.email, ''), COALESCE(m.role, ''), COALESCE(s.role_key, ''), s.permission_key,
				s.source, s.granted_by, s.subject_type, s.subject_id, TRUE
			FROM subjects s
			JOIN project p ON p.id::text = s.project_id
			JOIN issue i ON i.project_id = p.id
			LEFT JOIN "user" u ON u.id::text = NULLIF(s.effective_user_id, '')
			LEFT JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id::text = NULLIF(s.effective_user_id, '')
			WHERE s.issue_id = ''
		), owner_users AS (
			SELECT m.workspace_id::text AS workspace_id, m.user_id::text AS user_id
			FROM member m
			WHERE m.workspace_id = $1 AND m.role = 'owner'
		), owner_rows AS (
			SELECT 'project'::text AS scope, p.id::text AS project_id, p.title AS project_title,
				''::text AS issue_id, ''::text AS issue_title, ou.user_id,
				COALESCE(u.name, '') AS user_name, COALESCE(u.email, '') AS user_email, m.role AS workspace_role,
				''::text AS project_role, pm.permission, 'workspace_role'::text AS source,
				''::text AS granted_by, 'user'::text AS subject_type, m.user_id::text AS subject_id,
				FALSE AS inherited_from_project
			FROM project p
			JOIN owner_users ou ON ou.workspace_id = p.workspace_id::text
			JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id::text = ou.user_id
			JOIN "user" u ON u.id::text = ou.user_id
			CROSS JOIN (VALUES ('project.view'), ('project.edit'), ('project.issue.create'),
				('project.issue.manage'), ('project.agent.use'), ('project.member.manage'),
				('project.settings.manage')) AS pm(permission)
			WHERE p.workspace_id = $1

			UNION ALL

			SELECT 'issue'::text AS scope, p.id::text AS project_id, p.title AS project_title,
				i.id::text AS issue_id, i.title AS issue_title, ou.user_id,
				COALESCE(u.name, '') AS user_name, COALESCE(u.email, '') AS user_email, 'owner'::text AS workspace_role,
				''::text AS project_role, pm.permission, 'workspace_role'::text AS source,
				''::text AS granted_by, 'user'::text AS subject_type, ou.user_id AS subject_id,
				TRUE AS inherited_from_project
			FROM issue i
			JOIN project p ON p.id = i.project_id
			JOIN owner_users ou ON ou.workspace_id = p.workspace_id::text
			JOIN "user" u ON u.id::text = ou.user_id
			CROSS JOIN (VALUES ('project.view'), ('project.edit'), ('project.issue.create'),
				('project.issue.manage'), ('project.agent.use'), ('project.member.manage'),
				('project.settings.manage')) AS pm(permission)
			WHERE p.workspace_id = $1
		), all_rows AS (
			SELECT scope, project_id, project_title, issue_id, issue_title, user_id,
				user_name, user_email, workspace_role, project_role, permission, source,
				granted_by, subject_type, subject_id, inherited_from_project
			FROM report_rows
			UNION ALL
			SELECT scope, project_id, project_title, issue_id, issue_title, user_id,
				user_name, user_email, workspace_role, project_role, permission, source,
				granted_by, subject_type, subject_id, inherited_from_project
			FROM owner_rows
		), filtered_rows AS (
			SELECT DISTINCT * FROM all_rows
			WHERE ($2 = '' OR project_id = $2)
			  AND ($3 = '' OR issue_id = $3)
			  AND ($4 = '' OR user_id = $4)
			  AND ($5 = '' OR workspace_role = $5 OR project_role = $5)
			  AND ($6 = '' OR permission = $6)
			  AND ($7 = '' OR subject_type = $7)
			  AND ($8 = '' OR subject_id = $8)
			  AND ($9 = 'all' OR scope = $9)
		)
		SELECT scope, project_id, project_title, issue_id, issue_title,
			user_id, user_name, user_email, workspace_role, project_role,
			permission, source, granted_by, subject_type, subject_id,
			inherited_from_project, COUNT(*) OVER() AS total_count
		FROM filtered_rows
		ORDER BY project_title, project_id, issue_title, issue_id, user_name, permission, source
		LIMIT $10 OFFSET $11`,
		filter.WorkspaceID, filter.ProjectID, filter.IssueID, filter.UserID,
		filter.Role, string(filter.Permission), string(filter.SubjectType), filter.SubjectID,
		filter.Scope, filter.Limit, filter.Offset)
	if err != nil {
		return projectauth.PermissionReportResult{}, wrapProjectPermissionRepositoryError(err)
	}
	defer rows.Close()

	result := projectauth.PermissionReportResult{Rows: make([]projectauth.PermissionReportRow, 0)}
	for rows.Next() {
		var row projectauth.PermissionReportRow
		var issueID, issueTitle, projectRole, grantedBy, subjectID pgtype.Text
		var workspaceRole, permission, source, subjectType pgtype.Text
		var inherited bool
		var total int64
		if err := rows.Scan(&row.Scope, &row.ProjectID, &row.ProjectTitle, &issueID, &issueTitle,
			&row.UserID, &row.UserName, &row.UserEmail, &workspaceRole, &projectRole,
			&permission, &source, &grantedBy, &subjectType, &subjectID, &inherited, &total); err != nil {
			return projectauth.PermissionReportResult{}, wrapProjectPermissionRepositoryError(err)
		}
		if issueID.Valid {
			row.IssueID = issueID.String
		}
		if issueTitle.Valid {
			row.IssueTitle = issueTitle.String
		}
		if subjectType.Valid {
			row.SubjectType = projectauth.SubjectType(subjectType.String)
		}
		if subjectID.Valid {
			row.SubjectID = subjectID.String
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
		row.InheritedFromProject = inherited
		result.Rows = append(result.Rows, row)
		result.Total = total
	}
	return result, wrapProjectPermissionRepositoryError(rows.Err())
}
