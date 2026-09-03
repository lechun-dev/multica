package projectauth

import (
	"context"
	"errors"
	"testing"
)

type fakeRoleRepo struct {
	fakeRepo
	roles       map[string]RoleDefinition
	permissions map[ProjectRole][]Permission
}

// 2026-08-31 coder(lq): Keep role mutation audit assertions independent from
// the PostgreSQL adapter while exercising the same optional repository seam.
type auditRoleRepo struct {
	*fakeRoleRepo
	auditEvents []AuthorizationAuditEvent
	auditErr    error
}

func (f *auditRoleRepo) RecordAuthorizationAudit(_ context.Context, event AuthorizationAuditEvent) error {
	f.auditEvents = append(f.auditEvents, event)
	return f.auditErr
}

func (f *fakeRoleRepo) RolePermissions(_ context.Context, _ string, role ProjectRole) ([]Permission, bool, error) {
	permissions, found := f.permissions[role]
	return permissions, found, nil
}

func (f *fakeRoleRepo) ListRoleDefinitions(_ context.Context, _ string) ([]RoleDefinition, error) {
	result := make([]RoleDefinition, 0, len(f.roles))
	for _, role := range f.roles {
		result = append(result, role)
	}
	return result, nil
}

func (f *fakeRoleRepo) GetRoleDefinition(_ context.Context, _, key string) (RoleDefinition, error) {
	role, ok := f.roles[key]
	if !ok {
		return RoleDefinition{}, ErrInvalidRole
	}
	return role, nil
}

func (f *fakeRoleRepo) CreateRoleDefinition(_ context.Context, _, _ string, role RoleDefinition) (RoleDefinition, error) {
	f.roles[string(role.Key)] = role
	f.permissions[role.Key] = role.Permissions
	return role, nil
}

func (f *fakeRoleRepo) UpdateRoleDefinition(_ context.Context, _, key string, role RoleDefinition) (RoleDefinition, error) {
	current := f.roles[key]
	role.Key = ProjectRole(key)
	role.IsSystem = current.IsSystem
	f.roles[key] = role
	f.permissions[role.Key] = role.Permissions
	return role, nil
}

func (f *fakeRoleRepo) DeleteRoleDefinition(_ context.Context, _, key string) error {
	delete(f.roles, key)
	delete(f.permissions, ProjectRole(key))
	return nil
}

func TestCheckUsesDatabasePermissionsForCustomRole(t *testing.T) {
	repo := &fakeRoleRepo{
		fakeRepo:    fakeRepo{workspace: string(WorkspaceMember), project: "reviewer", projectWorkspace: "ws-1"},
		roles:       map[string]RoleDefinition{"reviewer": {Key: "reviewer", Name: "Reviewer"}},
		permissions: map[ProjectRole][]Permission{"reviewer": {View}},
	}
	s := New(repo, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	if err := s.Check(context.Background(), subject, "p-1", View); err != nil {
		t.Fatalf("custom role view: %v", err)
	}
	if err := s.Check(context.Background(), subject, "p-1", Edit); err == nil {
		t.Fatal("custom role must not inherit the default edit permission")
	}
}

func TestCheckTreatsEmptyDatabasePermissionsAsAuthoritative(t *testing.T) {
	repo := &fakeRoleRepo{
		fakeRepo:    fakeRepo{workspace: string(WorkspaceMember), project: string(ProjectMember), projectWorkspace: "ws-1"},
		roles:       map[string]RoleDefinition{"member": {Key: ProjectMember, Name: "Member", IsSystem: true}},
		permissions: map[ProjectRole][]Permission{ProjectMember: {}},
	}
	if err := New(repo, true).Check(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "p-1", View); err == nil {
		t.Fatal("an empty persisted role must deny access instead of falling back to defaults")
	}
}

func TestUpdateSystemRoleDoesNotRequireSystemMarkerInRequest(t *testing.T) {
	repo := &fakeRoleRepo{
		fakeRepo: fakeRepo{workspace: string(WorkspaceOwner)},
		roles: map[string]RoleDefinition{
			"member": {Key: ProjectMember, Name: "Member", IsSystem: true, Permissions: []Permission{View}},
		},
		permissions: map[ProjectRole][]Permission{ProjectMember: {View}},
	}
	updated, err := New(repo, true).UpdateRole(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "member", RoleDefinition{Name: "Collaborator", Permissions: []Permission{Edit}})
	if err != nil {
		t.Fatalf("update system role: %v", err)
	}
	if !updated.IsSystem || !repo.roles["member"].IsSystem {
		t.Fatal("system role marker must remain immutable")
	}
}

func TestListRolesAllowsWorkspaceMembersToReadCatalog(t *testing.T) {
	repo := &fakeRoleRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember)}, roles: map[string]RoleDefinition{}}
	if _, err := New(repo, true).ListRoles(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("workspace member role catalog read: %v", err)
	}
}

func TestRoleDefinitionsCanOnlyBeManagedByWorkspaceOwner(t *testing.T) {
	ctx := context.Background()
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}

	for _, workspaceRole := range []WorkspaceRole{WorkspaceAdmin, WorkspaceMember} {
		t.Run(string(workspaceRole), func(t *testing.T) {
			repo := &fakeRoleRepo{
				fakeRepo: fakeRepo{workspace: string(workspaceRole)},
				roles: map[string]RoleDefinition{
					"reviewer": {Key: "reviewer", Name: "Reviewer"},
				},
				permissions: map[ProjectRole][]Permission{"reviewer": {View}},
			}
			service := New(repo, true)

			if _, err := service.CreateRole(ctx, subject, RoleDefinition{Key: "auditor", Name: "Auditor", Permissions: []Permission{View}}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("create role error = %v, want %v", err, ErrForbidden)
			}
			if _, err := service.UpdateRole(ctx, subject, "reviewer", RoleDefinition{Name: "Updated", Permissions: []Permission{View}}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("update role error = %v, want %v", err, ErrForbidden)
			}
			if err := service.DeleteRole(ctx, subject, "reviewer"); !errors.Is(err, ErrForbidden) {
				t.Fatalf("delete role error = %v, want %v", err, ErrForbidden)
			}
		})
	}
}

func TestWorkspaceOwnerCanManageRoleDefinitions(t *testing.T) {
	repo := &fakeRoleRepo{
		fakeRepo:    fakeRepo{workspace: string(WorkspaceOwner)},
		roles:       map[string]RoleDefinition{},
		permissions: map[ProjectRole][]Permission{},
	}
	service := New(repo, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}

	if _, err := service.CreateRole(context.Background(), subject, RoleDefinition{Key: "reviewer", Name: "Reviewer", Permissions: []Permission{View}}); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := service.UpdateRole(context.Background(), subject, "reviewer", RoleDefinition{Name: "Auditor", Permissions: []Permission{View}}); err != nil {
		t.Fatalf("update role: %v", err)
	}
	if err := service.DeleteRole(context.Background(), subject, "reviewer"); err != nil {
		t.Fatalf("delete role: %v", err)
	}
}

func TestRoleMutationsRecordAudit(t *testing.T) {
	repo := &auditRoleRepo{fakeRoleRepo: &fakeRoleRepo{
		fakeRepo:    fakeRepo{workspace: string(WorkspaceOwner)},
		roles:       map[string]RoleDefinition{},
		permissions: map[ProjectRole][]Permission{},
	}}
	service := New(repo, true)
	owner := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}

	if _, err := service.CreateRole(context.Background(), owner, RoleDefinition{Key: "auditor", Name: "Auditor", Permissions: []Permission{View}}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := service.UpdateRole(context.Background(), owner, "auditor", RoleDefinition{Name: "Audit", Permissions: []Permission{View, Edit}}); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if err := service.DeleteRole(context.Background(), owner, "auditor"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if len(repo.auditEvents) != 3 {
		t.Fatalf("audit event count = %d, want 3", len(repo.auditEvents))
	}
	want := []string{"project_permission_role_created", "project_permission_role_updated", "project_permission_role_deleted"}
	for i, action := range want {
		if repo.auditEvents[i].Action != action {
			t.Fatalf("audit action[%d] = %q, want %q", i, repo.auditEvents[i].Action, action)
		}
	}
}

func TestRoleMutationReturnsAuditFailure(t *testing.T) {
	auditErr := errors.New("audit unavailable")
	repo := &auditRoleRepo{
		fakeRoleRepo: &fakeRoleRepo{
			fakeRepo:    fakeRepo{workspace: string(WorkspaceOwner)},
			roles:       map[string]RoleDefinition{},
			permissions: map[ProjectRole][]Permission{},
		},
		auditErr: auditErr,
	}
	service := New(repo, true)
	owner := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	if _, err := service.CreateRole(context.Background(), owner, RoleDefinition{Key: "auditor", Name: "Auditor", Permissions: []Permission{View}}); !errors.Is(err, auditErr) {
		t.Fatalf("CreateRole error = %v, want audit failure", err)
	}
}
