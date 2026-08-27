package projectauth

import (
	"context"
	"testing"
)

type fakeMemberRepo struct {
	fakeRepo
	addedProject    string
	addedUser       string
	addedRole       ProjectRole
	promotedProject string
	promotedUser    string
	promotedRole    ProjectRole
	members         []ProjectMemberRecord
	removed         bool
}

func (f *fakeMemberRepo) AddProjectMember(_ context.Context, projectID, userID string, role ProjectRole) error {
	f.addedProject, f.addedUser, f.addedRole = projectID, userID, role
	return nil
}

func (f *fakeMemberRepo) PromoteProjectMember(_ context.Context, projectID, userID string, role ProjectRole) error {
	f.promotedProject, f.promotedUser, f.promotedRole = projectID, userID, role
	return nil
}

func (f *fakeMemberRepo) RemoveProjectMember(context.Context, string, string) error {
	f.removed = true
	return nil
}

func (f *fakeMemberRepo) ListProjectMembers(context.Context, string) ([]ProjectMemberRecord, error) {
	return f.members, nil
}

type fakeRepo struct {
	workspace, project, projectWorkspace string
	projects                             []string
	workspaceErr, projectErr             error
}

func (f fakeRepo) WorkspaceRole(context.Context, string, string) (WorkspaceRole, error) {
	return WorkspaceRole(f.workspace), f.workspaceErr
}
func (f fakeRepo) ProjectRole(context.Context, string, string) (ProjectRole, error) {
	return ProjectRole(f.project), f.projectErr
}
func (f fakeRepo) ProjectWorkspace(context.Context, string) (string, error) {
	return f.projectWorkspace, nil
}
func (f fakeRepo) VisibleProjectIDs(context.Context, string, string) ([]string, error) {
	return f.projects, nil
}

func TestPolicyInheritanceAndRoles(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		ws   WorkspaceRole
		pr   ProjectRole
		p    Permission
		ok   bool
	}{
		{"workspace owner manages project members", WorkspaceOwner, "", MemberManage, true},
		{"workspace admin cannot manage project members", WorkspaceAdmin, "", MemberManage, false},
		{"workspace admin without project grant cannot view", WorkspaceAdmin, "", View, false},
		{"workspace admin without project grant cannot manage settings", WorkspaceAdmin, "", SettingsManage, false},
		{"workspace admin with project member grant can view", WorkspaceAdmin, ProjectViewer, View, true},
		{"project owner manages project members", WorkspaceMember, ProjectOwner, MemberManage, true},
		{"project manager cannot manage project members", WorkspaceMember, ProjectManager, MemberManage, false},
		{"viewer read", WorkspaceMember, ProjectViewer, View, true},
		{"viewer cannot edit", WorkspaceMember, ProjectViewer, Edit, false},
		{"member creates issue", WorkspaceMember, ProjectMember, IssueCreate, true},
		{"member cannot manage members", WorkspaceMember, ProjectMember, MemberManage, false},
		{"manager manages issue", WorkspaceMember, ProjectManager, IssueManage, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(fakeRepo{workspace: string(tc.ws), project: string(tc.pr), projectWorkspace: "ws-1"}, true)
			err := s.Check(ctx, Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "p-1", tc.p)
			if (err == nil) != tc.ok {
				t.Fatalf("got %v, want allowed=%v", err, tc.ok)
			}
		})
	}
}

func TestDisabledPreservesLegacyBehavior(t *testing.T) {
	s := New(nil, false)
	if err := s.Check(context.Background(), Subject{}, "", Edit); err != nil {
		t.Fatal(err)
	}
}

func TestEnabledWithoutRepositoryFailsClosed(t *testing.T) {
	s := New(nil, true)
	if err := s.Check(context.Background(), Subject{UserID: "u", WorkspaceID: "w"}, "p", View); err != ErrDisabled {
		t.Fatalf("got %v, want %v", err, ErrDisabled)
	}
	if _, err := s.Scope(context.Background(), Subject{UserID: "u", WorkspaceID: "w"}); err != ErrDisabled {
		t.Fatalf("scope got %v, want %v", err, ErrDisabled)
	}
}

func TestEnsureOwnerSeedsCreator(t *testing.T) {
	repo := &fakeMemberRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1"}}
	s := New(repo, true)
	if err := s.EnsureOwner(context.Background(), "p-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	if repo.promotedProject != "p-1" || repo.promotedUser != "u-1" || repo.promotedRole != ProjectOwner {
		t.Fatalf("unexpected seed: project=%q user=%q role=%q", repo.promotedProject, repo.promotedUser, repo.promotedRole)
	}
}

func TestPromoteMemberUsesMinimumRole(t *testing.T) {
	repo := &fakeMemberRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1"}}
	s := New(repo, true)
	if err := s.PromoteMember(context.Background(), "p-1", "u-1", ProjectMember); err != nil {
		t.Fatal(err)
	}
	if repo.promotedProject != "p-1" || repo.promotedUser != "u-1" || repo.promotedRole != ProjectMember {
		t.Fatalf("unexpected promotion: project=%q user=%q role=%q", repo.promotedProject, repo.promotedUser, repo.promotedRole)
	}
}

func TestPromoteMemberDisabledIsNoop(t *testing.T) {
	repo := &fakeMemberRepo{}
	if err := New(repo, false).PromoteMember(context.Background(), "p-1", "u-1", ProjectViewer); err != nil {
		t.Fatal(err)
	}
	if repo.promotedUser != "" {
		t.Fatal("disabled project permissions must not persist automatic membership")
	}
}

func TestPromoteMemberRejectsInvalidRole(t *testing.T) {
	repo := &fakeMemberRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1"}}
	err := New(repo, true).PromoteMember(context.Background(), "p-1", "u-1", ProjectRole("unknown"))
	if err != ErrInvalidRole {
		t.Fatalf("got %v, want %v", err, ErrInvalidRole)
	}
}

func TestRemoveMemberProtectsLastOwner(t *testing.T) {
	repo := &fakeMemberRepo{
		fakeRepo: fakeRepo{workspace: string(WorkspaceMember), project: string(ProjectOwner), projectWorkspace: "ws-1"},
		members:  []ProjectMemberRecord{{ProjectID: "p-1", UserID: "u-1", Role: ProjectOwner}},
	}
	s := New(repo, true)
	err := s.RemoveMember(context.Background(), Subject{UserID: "u-2", WorkspaceID: "ws-1"}, "p-1", "u-1")
	if err != ErrLastOwner {
		t.Fatalf("got %v, want %v", err, ErrLastOwner)
	}
	if repo.removed {
		t.Fatal("last owner must not be removed")
	}
}

// 2026-08-27 coder(lq): Changing a member's role uses the same endpoint as
// adding one, so it must preserve the final project owner just like removal.
func TestAddMemberProtectsLastOwnerFromDowngrade(t *testing.T) {
	repo := &fakeMemberRepo{
		fakeRepo: fakeRepo{workspace: string(WorkspaceMember), project: string(ProjectOwner), projectWorkspace: "ws-1"},
		members:  []ProjectMemberRecord{{ProjectID: "p-1", UserID: "u-1", Role: ProjectOwner}},
	}
	s := New(repo, true)
	err := s.AddMember(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "p-1", "u-1", ProjectMember)
	if err != ErrLastOwner {
		t.Fatalf("got %v, want %v", err, ErrLastOwner)
	}
	if repo.addedUser != "" {
		t.Fatal("last owner must not be downgraded")
	}
}
