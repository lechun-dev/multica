package projectauth

import "context"

// 2026-08-24 coder(lq): Keep the persistence boundary narrow. Keeping it
// separate from sqlc's generated Queries makes upstream rebases low-friction.
type Repository interface {
	WorkspaceRole(ctx context.Context, workspaceID, userID string) (WorkspaceRole, error)
	ProjectRole(ctx context.Context, projectID, userID string) (ProjectRole, error)
	ProjectWorkspace(ctx context.Context, projectID string) (string, error)
	VisibleProjectIDs(ctx context.Context, workspaceID, userID string) ([]string, error)
}

// WorkspaceOwnerBypassReader exposes the workspace-level switch that controls
// whether workspace owners inherit access to every project. Repositories that
// predate the switch may omit this optional interface and retain the legacy
// enabled-by-default behavior.
type WorkspaceOwnerBypassReader interface {
	WorkspaceOwnerBypassEnabled(ctx context.Context, workspaceID string) (bool, error)
}

// IssuePermissionReader is an optional task-level grant lookup.
type IssuePermissionReader interface {
	IssuePermission(ctx context.Context, issueID, userID string, permission Permission) (bool, error)
}

// 2026-08-28 coder(lq): ScopedProjectRepository optionally hides projects that
// are visible only through workspace ownership. Additive shape preserves old
// test/adapter implementations when the list toggle is unused.
type ScopedProjectRepository interface {
	VisibleProjectIDsWithWorkspaceScope(ctx context.Context, workspaceID, userID string, includeWorkspaceOwned bool) ([]string, error)
}

type RolePermissionRepository interface {
	RolePermissions(ctx context.Context, workspaceID string, role ProjectRole) (permissions []Permission, found bool, err error)
}

// ProjectRoleReader is an optional batch read surface used by list views. It
// keeps the SQL adapter outside this package while avoiding one permission
// query per project row.
// 2026-08-28 coder(lq): Add a batch role lookup for project list metadata.
type ProjectRoleReader interface {
	CurrentProjectRoles(ctx context.Context, workspaceID, userID string) (map[string]ProjectRole, error)
}

// GrantRepository is the additive persistence seam for the unified grants
// table. Legacy Repository methods remain available during migration.
type GrantRepository interface {
	Repository
	ListAccessGrants(ctx context.Context, workspaceID, projectID, issueID string) ([]AccessGrant, error)
	ListUserOrganizations(ctx context.Context, workspaceID, userID string) ([]string, error)
	UpsertAccessGrant(ctx context.Context, grant AccessGrant) error
	DeleteAccessGrant(ctx context.Context, workspaceID, projectID, issueID string, subjectType SubjectType, subjectID string, role ProjectRole, permission Permission) error
}

// AuthorizationAuditEvent is the storage-neutral audit record emitted for
// authorization mutations. The Handler adapter persists it in Multica's
// existing activity_log table; keeping the event here avoids coupling the
// authorization package to PostgreSQL or generated sqlc models.
// 2026-08-31 coder(lq): Add one audit seam for grants and role definitions so
// every authorization write can be committed atomically with its audit row.
type AuthorizationAuditEvent struct {
	WorkspaceID string
	ProjectID   string
	IssueID     string
	ActorUserID string
	Action      string
	Details     map[string]any
}

type AuditRepository interface {
	RecordAuthorizationAudit(ctx context.Context, event AuthorizationAuditEvent) error
}

// AccessGrantReader is an optional read-after-write seam. HTTP adapters can
// return the canonical persisted grant (including its generated ID) without
// coupling the authorization package to a database driver.
// 2026-08-31 coder(lq): Keep POST grant responses consistent with the
// provider-neutral API contract while older adapters remain source-compatible.
type AccessGrantReader interface {
	GetAccessGrant(ctx context.Context, workspaceID, projectID, issueID string,
		subjectType SubjectType, subjectID string, role ProjectRole, permission Permission) (AccessGrant, error)
}

// ResourceRepository is an optional consistency seam for adapters that can
// verify task ownership before reading or writing a task grant. Keeping it
// optional lets older test/dry-run adapters compile while production adapters
// fail closed when the resource cannot be resolved.
// 2026-08-31 coder(lq): Validate task-to-project binding at the authorization
// boundary instead of relying on callers to pass a matching project ID.
type ResourceRepository interface {
	IssueProject(ctx context.Context, issueID string) (workspaceID, projectID string, err error)
}

// SubjectRepository is an optional provider-neutral directory boundary. The
// authorization core never calls DingTalk, WeCom, Feishu, or another OA API;
// adapters expose the last synchronized MissionOS directory snapshot here.
// 2026-09-01 coder(lq): Validate grant subjects before persisting them so a
// stale or cross-workspace external identifier cannot create an unusable ACL.
type SubjectRepository interface {
	UserInWorkspace(ctx context.Context, workspaceID, userID string) (bool, error)
	ActiveOrganizationInWorkspace(ctx context.Context, workspaceID, organizationID string) (bool, error)
}

// OrganizationDirectoryRepository is intentionally separate from Repository:
// older adapters can continue authorizing existing grants without having to
// implement a directory listing before the organization picker is enabled.
// 2026-09-01 coder(lq): Keep organization synchronization/storage additive.
type OrganizationDirectoryRepository interface {
	ListOrganizations(ctx context.Context, workspaceID string) ([]Organization, error)
}

// OrganizationMemberDirectoryRepository is optional so adapters which only
// support the original organization picker remain source-compatible.
// 2026-09-03 coder(lq): Keep employee directory reads additive and isolated.
type OrganizationMemberDirectoryRepository interface {
	ListOrganizationMembers(ctx context.Context, workspaceID string) ([]OrganizationMember, error)
}

type RoleDefinition struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	Key         ProjectRole  `json:"key"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	IsSystem    bool         `json:"is_system"`
	Permissions []Permission `json:"permissions"`
}

type RoleRepository interface {
	Repository
	ListRoleDefinitions(ctx context.Context, workspaceID string) ([]RoleDefinition, error)
	GetRoleDefinition(ctx context.Context, workspaceID, key string) (RoleDefinition, error)
	CreateRoleDefinition(ctx context.Context, workspaceID, createdBy string, role RoleDefinition) (RoleDefinition, error)
	UpdateRoleDefinition(ctx context.Context, workspaceID, key string, role RoleDefinition) (RoleDefinition, error)
	DeleteRoleDefinition(ctx context.Context, workspaceID, key string) error
}
