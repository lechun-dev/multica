package projectauth

import (
	"context"
	"fmt"
	"strings"
)

type Service struct {
	repo    Repository
	policy  Policy
	enabled bool
}

func New(repo Repository, enabled bool) *Service {
	return &Service{repo: repo, policy: DefaultPolicy(), enabled: enabled}
}

func (s *Service) Enabled() bool { return s != nil && s.enabled }

// 2026-08-24 coder(lq): Return nil only when the subject may perform the
// permission; disabled deployments preserve legacy behavior during rollout.
func (s *Service) Check(ctx context.Context, subject Subject, projectID string, permission Permission) error {
	if s == nil || !s.enabled {
		return nil
	}
	// 2026-08-24 coder(lq): Fail closed when the rollout flag is enabled but
	// the persistence adapter was not wired, instead of panicking in a request.
	if s.repo == nil {
		return ErrDisabled
	}
	if subject.UserID == "" || subject.WorkspaceID == "" || projectID == "" {
		return ErrNoProjectAccess
	}
	workspaceID, err := s.repo.ProjectWorkspace(ctx, projectID)
	if err != nil || workspaceID != subject.WorkspaceID {
		return ErrNoProjectAccess
	}
	role, err := s.repo.WorkspaceRole(ctx, subject.WorkspaceID, subject.UserID)
	if err != nil {
		return ErrNotWorkspaceMember
	}
	if role == WorkspaceOwner {
		return nil
	}
	projectRole, err := s.repo.ProjectRole(ctx, projectID, subject.UserID)
	if err != nil {
		return ErrNoProjectAccess
	}
	// 2026-08-27 coder(lq): Workspace admin is not a project grant. Once the
	// project-permission overlay is enabled, every non-owner must have an
	// explicit project_members row before any project operation is allowed.
	// Keep this check explicit so future policy additions cannot accidentally
	// make a project manager/member able to administer membership.
	allowed := s.policy.Allows(projectRole, permission)
	if resolver, ok := s.repo.(RolePermissionRepository); ok {
		permissions, found, resolveErr := resolver.RolePermissions(ctx, subject.WorkspaceID, projectRole)
		if resolveErr != nil {
			return ErrForbidden
		}
		// Role rows are authoritative, including an intentionally empty set.
		if found {
			allowed = false
			for _, candidate := range permissions {
				if candidate == permission {
					allowed = true
					break
				}
			}
		}
	}
	if !allowed {
		return fmt.Errorf("%w: role=%s permission=%s", ErrForbidden, projectRole, permission)
	}
	return nil
}

func (s *Service) ListRoles(ctx context.Context, subject Subject) ([]RoleDefinition, error) {
	rr, ok := s.repo.(RoleRepository)
	if !ok {
		return nil, ErrDisabled
	}
	// 2026-08-28 coder(lq): Project owners need to read the workspace role
	// catalog when granting project access; only the workspace owner may mutate it.
	if _, err := s.requireWorkspaceMember(ctx, subject); err != nil {
		return nil, err
	}
	return rr.ListRoleDefinitions(ctx, subject.WorkspaceID)
}

func (s *Service) CreateRole(ctx context.Context, subject Subject, role RoleDefinition) (RoleDefinition, error) {
	rr, ok := s.repo.(RoleRepository)
	if !ok {
		return RoleDefinition{}, ErrDisabled
	}
	if err := s.requireRoleOwner(ctx, subject); err != nil {
		return RoleDefinition{}, err
	}
	if role.IsSystem || !validCustomRoleKey(role.Key) {
		return RoleDefinition{}, ErrInvalidRole
	}
	if err := validatePermissions(role.Permissions); err != nil {
		return RoleDefinition{}, err
	}
	return rr.CreateRoleDefinition(ctx, subject.WorkspaceID, subject.UserID, role)
}

func (s *Service) UpdateRole(ctx context.Context, subject Subject, key string, role RoleDefinition) (RoleDefinition, error) {
	rr, ok := s.repo.(RoleRepository)
	if !ok {
		return RoleDefinition{}, ErrDisabled
	}
	if err := s.requireRoleOwner(ctx, subject); err != nil {
		return RoleDefinition{}, err
	}
	if key == "" {
		return RoleDefinition{}, ErrInvalidRole
	}
	current, err := rr.GetRoleDefinition(ctx, subject.WorkspaceID, key)
	if err != nil {
		return RoleDefinition{}, ErrInvalidRole
	}
	// The key and system marker are immutable identity fields. System roles
	// are editable, but remain undeletable after their permissions change.
	if role.Key != "" && string(role.Key) != key {
		return RoleDefinition{}, ErrInvalidRole
	}
	role.Key = current.Key
	role.IsSystem = current.IsSystem
	if err := validatePermissions(role.Permissions); err != nil {
		return RoleDefinition{}, err
	}
	return rr.UpdateRoleDefinition(ctx, subject.WorkspaceID, key, role)
}

func (s *Service) DeleteRole(ctx context.Context, subject Subject, key string) error {
	rr, ok := s.repo.(RoleRepository)
	if !ok {
		return ErrDisabled
	}
	if err := s.requireRoleOwner(ctx, subject); err != nil {
		return err
	}
	if key == "" {
		return ErrInvalidRole
	}
	definition, err := rr.GetRoleDefinition(ctx, subject.WorkspaceID, key)
	if err != nil || definition.IsSystem {
		return ErrInvalidRole
	}
	return rr.DeleteRoleDefinition(ctx, subject.WorkspaceID, key)
}

func (s *Service) requireRoleOwner(ctx context.Context, subject Subject) error {
	role, err := s.requireWorkspaceMember(ctx, subject)
	if err != nil {
		return err
	}
	// 2026-08-28 coder(lq): Role definitions affect authorization across the
	// whole workspace, so workspace admins and project owners may only consume
	// the catalog; only the workspace owner may mutate it.
	if role != WorkspaceOwner {
		return ErrForbidden
	}
	return nil
}

func (s *Service) requireWorkspaceMember(ctx context.Context, subject Subject) (WorkspaceRole, error) {
	if subject.UserID == "" || subject.WorkspaceID == "" || s == nil || s.repo == nil {
		return "", ErrNotWorkspaceMember
	}
	role, err := s.repo.WorkspaceRole(ctx, subject.WorkspaceID, subject.UserID)
	if err != nil {
		return "", ErrNotWorkspaceMember
	}
	return role, nil
}

func validatePermissions(permissions []Permission) error {
	for _, permission := range permissions {
		if !validReportPermission(permission) {
			return fmt.Errorf("%w: permission=%s", ErrInvalidRole, permission)
		}
	}
	return nil
}

func validCustomRoleKey(role ProjectRole) bool {
	value := strings.TrimSpace(string(role))
	if value == "" || len(value) > 64 || IsSystemRole(ProjectRole(value)) {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (char == '-' && index > 0 && index < len(value)-1) {
			continue
		}
		return false
	}
	return true
}

// 2026-08-24 coder(lq): Keep the HTTP adapter on one authorization entry point.
func (s *Service) Require(ctx context.Context, subject Subject, projectID string, permission Permission) error {
	return s.Check(ctx, subject, projectID, permission)
}

// 2026-08-24 coder(lq): Scope project lists to native admins or project_members.
func (s *Service) Scope(ctx context.Context, subject Subject) ([]string, error) {
	if s == nil || !s.enabled {
		return nil, nil
	}
	if s.repo == nil {
		return nil, ErrDisabled
	}
	if subject.UserID == "" || subject.WorkspaceID == "" {
		return nil, ErrNotWorkspaceMember
	}
	_, err := s.repo.WorkspaceRole(ctx, subject.WorkspaceID, subject.UserID)
	if err != nil {
		return nil, ErrNotWorkspaceMember
	}
	return s.repo.VisibleProjectIDs(ctx, subject.WorkspaceID, subject.UserID)
}

func ValidateProjectRole(role ProjectRole) error {
	if !validProjectRole(role) {
		return fmt.Errorf("%w: %s", ErrInvalidRole, role)
	}
	return nil
}
