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
