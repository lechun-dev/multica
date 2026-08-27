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

type RolePermissionRepository interface {
	RolePermissions(ctx context.Context, workspaceID string, role ProjectRole) (permissions []Permission, found bool, err error)
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
