package projectauth

import (
	"context"
	"errors"
	"testing"
)

type fakeIssuePermissionRepo struct {
	fakeRepo
	grants map[string]bool
}

func (f *fakeIssuePermissionRepo) IssuePermission(_ context.Context, _, _ string, permission Permission) (bool, error) {
	return f.grants[string(permission)], nil
}

func TestCheckIssueInheritsProjectPermission(t *testing.T) {
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	service := New(&fakeRepo{
		workspace:        string(WorkspaceMember),
		project:          string(ProjectViewer),
		projectWorkspace: "ws-1",
	}, true)

	if err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", View); err != nil {
		t.Fatalf("project viewer should view a bound task: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", Edit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("project viewer editing a task got %v, want %v", err, ErrForbidden)
	}
}

// 2026-08-27 coder(lq): CheckIssue must preserve the exact project permission
// requested by the caller. This prevents a task endpoint from accidentally
// collapsing every operation to the project's View permission.
func TestCheckIssueDelegatesEveryPermissionToProject(t *testing.T) {
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	cases := []struct {
		name       string
		role       ProjectRole
		permission Permission
		want       bool
	}{
		{"viewer can view", ProjectViewer, View, true},
		{"viewer cannot edit", ProjectViewer, Edit, false},
		{"member can create issues", ProjectMember, IssueCreate, true},
		{"member can comment on issues", ProjectMember, IssueComment, true},
		{"viewer cannot comment on issues", ProjectViewer, IssueComment, false},
		{"member cannot manage issues", ProjectMember, IssueManage, false},
		{"manager can manage issues", ProjectManager, IssueManage, true},
		{"manager can use agents", ProjectManager, AgentUse, true},
		{"manager cannot manage members", ProjectManager, MemberManage, false},
		{"owner can manage settings", ProjectOwner, SettingsManage, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := New(&fakeRepo{
				workspace:        string(WorkspaceMember),
				project:          string(tc.role),
				projectWorkspace: "ws-1",
			}, true)
			err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", tc.permission)
			if (err == nil) != tc.want {
				t.Fatalf("CheckIssue(%s) got %v, want allowed=%v", tc.permission, err, tc.want)
			}
		})
	}
}

func TestCheckIssueRejectsUserWithoutProjectMembership(t *testing.T) {
	service := New(&fakeRepo{
		workspace:        string(WorkspaceMember),
		projectWorkspace: "ws-1",
		projectErr:       errors.New("project membership not found"),
	}, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}

	if err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", View); !errors.Is(err, ErrNoProjectAccess) {
		t.Fatalf("member without project access got %v, want %v", err, ErrNoProjectAccess)
	}
}

func TestCheckIssueRequiresProjectBinding(t *testing.T) {
	service := New(&fakeRepo{
		workspace:        string(WorkspaceMember),
		project:          string(ProjectOwner),
		projectWorkspace: "ws-1",
	}, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}

	for name, ids := range map[string][2]string{
		"missing issue":   {"", "project-1"},
		"missing project": {"issue-1", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.CheckIssue(context.Background(), subject, ids[0], ids[1], View); !errors.Is(err, ErrNoProjectAccess) {
				t.Fatalf("got %v, want %v", err, ErrNoProjectAccess)
			}
		})
	}
}

func TestCheckIssueAllowsMentionedTaskMember(t *testing.T) {
	service := New(&fakeIssuePermissionRepo{fakeRepo: fakeRepo{
		workspace:        string(WorkspaceMember),
		projectWorkspace: "ws-1",
		projectErr:       errors.New("project membership not found"),
	}, grants: map[string]bool{string(View): true, string(IssueComment): true}}, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}

	if err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", View); err != nil {
		t.Fatalf("mentioned task member should view task: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", IssueComment); err != nil {
		t.Fatalf("mentioned task member should comment: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "issue-1", "project-1", Edit); !errors.Is(err, ErrNoProjectAccess) {
		t.Fatalf("task member must not edit through task grant, got %v", err)
	}
}

func TestCheckIssueRejectsTaskMemberWithoutGrant(t *testing.T) {
	repo := &fakeIssuePermissionRepo{fakeRepo: fakeRepo{
		workspace:        string(WorkspaceMember),
		projectWorkspace: "ws-1",
		projectErr:       errors.New("project membership not found"),
	}, grants: map[string]bool{}}

	err := New(repo, true).CheckIssue(
		context.Background(),
		Subject{UserID: "u-1", WorkspaceID: "ws-1"},
		"issue-1",
		"project-1",
		View,
	)
	if !errors.Is(err, ErrNoProjectAccess) {
		t.Fatalf("legacy task grant must not bypass project membership, got %v", err)
	}
}
