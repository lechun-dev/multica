package projectauth

import (
	"context"
	"testing"
)

type fakeRoleRepo struct {
	fakeRepo
	roles       map[string]RoleDefinition
	permissions map[ProjectRole][]Permission
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
