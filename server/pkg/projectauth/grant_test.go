package projectauth

import (
	"context"
	"errors"
	"testing"
)

type fakeGrantRepo struct {
	fakeRepo
	grants              []AccessGrant
	orgs                []string
	upserted            *AccessGrant
	invalidUser         bool
	invalidOrganization bool
	subjectErr          error
}

// subjectlessGrantRepo exposes only the unified ACL contract. Embedding the
// interface (rather than the concrete fake) intentionally hides optional
// directory/resource methods for fail-closed tests.
type subjectlessGrantRepo struct{ GrantRepository }

// legacyOnlyRepo deliberately exposes only the pre-unified Repository
// contract. It proves an enabled deployment cannot silently fall back to
// project_members after the canonical grant migration is turned on.
type legacyOnlyRepo struct {
	workspace        WorkspaceRole
	projectWorkspace string
}

func (r legacyOnlyRepo) WorkspaceRole(context.Context, string, string) (WorkspaceRole, error) {
	return r.workspace, nil
}
func (r legacyOnlyRepo) ProjectRole(context.Context, string, string) (ProjectRole, error) {
	return ProjectOwner, nil
}
func (r legacyOnlyRepo) ProjectWorkspace(context.Context, string) (string, error) {
	return r.projectWorkspace, nil
}
func (r legacyOnlyRepo) VisibleProjectIDs(context.Context, string, string) ([]string, error) {
	return nil, nil
}

// 2026-08-31 coder(lq): Capture audit calls in-memory so authorization writes
// can prove they fail closed when the durable audit sink is unavailable.
type auditGrantRepo struct {
	*fakeGrantRepo
	auditEvents []AuthorizationAuditEvent
	auditErr    error
}

func (f *auditGrantRepo) RecordAuthorizationAudit(_ context.Context, event AuthorizationAuditEvent) error {
	f.auditEvents = append(f.auditEvents, event)
	return f.auditErr
}

func (f *fakeGrantRepo) ListAccessGrants(context.Context, string, string, string) ([]AccessGrant, error) {
	return f.grants, nil
}
func (f *fakeGrantRepo) ListUserOrganizations(context.Context, string, string) ([]string, error) {
	return f.orgs, nil
}
func (f *fakeGrantRepo) UpsertAccessGrant(_ context.Context, grant AccessGrant) error {
	f.upserted = &grant
	return nil
}
func (f *fakeGrantRepo) DeleteAccessGrant(context.Context, string, string, string, SubjectType, string, ProjectRole, Permission) error {
	return nil
}

func (f *fakeGrantRepo) UserInWorkspace(context.Context, string, string) (bool, error) {
	return !f.invalidUser, f.subjectErr
}

func (f *fakeGrantRepo) ActiveOrganizationInWorkspace(context.Context, string, string) (bool, error) {
	return !f.invalidOrganization, f.subjectErr
}

func TestUnifiedGrantsResolveUserOrganizationAndEveryone(t *testing.T) {
	ctx := context.Background()
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	cases := []struct {
		name  string
		grant AccessGrant
		orgs  []string
		want  bool
	}{
		{name: "user", grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: Edit}, want: true},
		{name: "organization", grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectOrganization, SubjectID: "org-1", Permission: Edit}, orgs: []string{"org-1"}, want: true},
		{name: "everyone", grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectEveryone, Permission: View}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1", projectErr: errors.New("legacy row absent")}, grants: []AccessGrant{tc.grant}, orgs: tc.orgs}
			if err := New(repo, true).Check(ctx, subject, "p-1", tc.grant.Permission); (err == nil) != tc.want {
				t.Fatalf("Check got %v, want allowed=%v", err, tc.want)
			}
		})
	}
}

func TestOrganizationGrantUsesEffectiveAncestorOrganizations(t *testing.T) {
	ctx := context.Background()
	subject := Subject{UserID: "u-child", WorkspaceID: "ws-1"}
	parentGrant := AccessGrant{ProjectID: "p-1", SubjectType: SubjectOrganization, SubjectID: "org-parent", Permission: View}
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1"}, grants: []AccessGrant{parentGrant}, orgs: []string{"org-child", "org-parent"}}
	if err := New(repo, true).Check(ctx, subject, "p-1", View); err != nil {
		t.Fatalf("child department did not inherit parent grant: %v", err)
	}

	repo.orgs = []string{"org-child", "org-other"}
	if err := New(repo, true).Check(ctx, subject, "p-1", View); err == nil {
		t.Fatal("unrelated department unexpectedly inherited parent grant")
	}
}

func TestEnabledAuthorizationRejectsLegacyOnlyRepository(t *testing.T) {
	repo := legacyOnlyRepo{workspace: WorkspaceMember, projectWorkspace: "ws-1"}
	err := New(repo, true).Check(
		context.Background(),
		Subject{UserID: "u-1", WorkspaceID: "ws-1"},
		"p-1",
		View,
	)
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("enabled authorization error = %v, want %v", err, ErrMigrationRequired)
	}
}

func TestTaskGrantCanAuthorizeOneTaskWithoutProjectView(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{
		workspace: string(WorkspaceMember), projectWorkspace: "ws-1",
		projectErr:   errors.New("no project membership"),
		issueProject: "p-1",
	}, grants: []AccessGrant{{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: Edit}}}
	service := New(repo, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	if err := service.CheckIssue(context.Background(), subject, "i-1", "p-1", Edit); err != nil {
		t.Fatalf("task grant should authorize the explicitly shared task: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "i-2", "p-1", Edit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("task grant leaked to another task: %v", err)
	}

	repo.project = string(ProjectViewer)
	repo.projectErr = nil
	repo.grants = append(repo.grants, AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: View})
	if err := service.CheckIssue(context.Background(), subject, "i-1", "p-1", Edit); err != nil {
		t.Fatalf("task grant should add edit to visible task: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "i-2", "p-1", Edit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("task grant leaked to another task: %v", err)
	}
}

func TestTaskGrantCannotAuthorizeProjectAdministration(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{
		workspace: string(WorkspaceMember), projectWorkspace: "ws-1", issueProject: "p-1",
	}, grants: []AccessGrant{{
		ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: MemberManage,
	}}}
	service := New(repo, true)
	err := service.CheckIssue(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "i-1", "p-1", MemberManage)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("task grant should not authorize project administration: %v", err)
	}
}

func TestTaskDirectGrantSupportsCommentAndArchiveOnlyOnThatTask(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{
		workspace: string(WorkspaceMember), projectWorkspace: "ws-1", issueProject: "p-1",
	}, grants: []AccessGrant{
		{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: View},
		{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: IssueComment},
		{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: IssueArchive},
	}}
	service := New(repo, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	for _, permission := range []Permission{IssueComment, IssueArchive} {
		if err := service.CheckIssue(context.Background(), subject, "i-1", "p-1", permission); err != nil {
			t.Fatalf("task direct grant for %s = %v", permission, err)
		}
		if err := service.CheckIssue(context.Background(), subject, "i-2", "p-1", permission); !errors.Is(err, ErrForbidden) {
			t.Fatalf("task direct grant for %s leaked to sibling: %v", permission, err)
		}
	}
}

func TestGrantAccessAllowsTaskCommentAndArchiveButRejectsProjectAdministration(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1", issueProject: "p-1"}}
	service := New(repo, true)
	actor := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	for _, permission := range []Permission{IssueComment, IssueArchive} {
		err := service.GrantAccess(context.Background(), actor, AccessGrant{
			ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: permission,
		})
		if err != nil {
			t.Fatalf("GrantAccess(%s) = %v", permission, err)
		}
	}
	for _, permission := range []Permission{IssueCreate, MemberManage, SettingsManage} {
		if err := service.GrantAccess(context.Background(), actor, AccessGrant{
			ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: permission,
		}); !errors.Is(err, ErrInvalidIssuePermission) {
			t.Fatalf("task %s grant = %v, want %v", permission, err, ErrInvalidIssuePermission)
		}
	}
}

func TestTaskDirectGrantCannotCreateAnotherTask(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{
		workspace: string(WorkspaceMember), projectWorkspace: "ws-1", issueProject: "p-1",
	}, grants: []AccessGrant{
		{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: View},
		// Simulate a legacy/manual row that was written before task grants
		// rejected project.issue.create. CheckIssue must still fail closed.
		{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: IssueCreate},
	}}
	service := New(repo, true)
	if err := service.CheckIssue(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "i-1", "p-1", IssueCreate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("task direct issue.create grant = %v, want %v", err, ErrForbidden)
	}
}

func TestRoleGrantUsesConfiguredPermissionSet(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1", project: string(ProjectViewer)}, grants: []AccessGrant{{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Role: ProjectManager}}}
	service := New(repo, true)
	if err := service.Check(context.Background(), Subject{UserID: "u-1", WorkspaceID: "ws-1"}, "p-1", IssueManage); err != nil {
		t.Fatalf("role grant should use default manager permissions: %v", err)
	}
}

func TestGrantAndRevokeAccessRecordAudit(t *testing.T) {
	repo := &auditGrantRepo{fakeGrantRepo: &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}}
	service := New(repo, true)
	actor := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	grant := AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: View}

	if err := service.GrantAccess(context.Background(), actor, grant); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	if err := service.RevokeAccess(context.Background(), actor, grant); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	if len(repo.auditEvents) != 2 {
		t.Fatalf("audit event count = %d, want 2", len(repo.auditEvents))
	}
	if repo.auditEvents[0].Action != "project_permission_granted" || repo.auditEvents[1].Action != "project_permission_revoked" {
		t.Fatalf("unexpected audit actions: %#v", repo.auditEvents)
	}
}

func TestGrantAndRevokeAccessReturnAuditFailure(t *testing.T) {
	repo := &auditGrantRepo{
		fakeGrantRepo: &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}},
		auditErr:      errors.New("audit unavailable"),
	}
	service := New(repo, true)
	actor := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	grant := AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: View}

	if err := service.GrantAccess(context.Background(), actor, grant); !errors.Is(err, repo.auditErr) {
		t.Fatalf("GrantAccess error = %v, want audit failure", err)
	}
	if err := service.RevokeAccess(context.Background(), actor, grant); !errors.Is(err, repo.auditErr) {
		t.Fatalf("RevokeAccess error = %v, want audit failure", err)
	}
}

func TestGrantAccessRequiresExactlyOneGrantKind(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}
	service := New(repo, true)
	actor := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	cases := []struct {
		name  string
		grant AccessGrant
		want  error
	}{
		{
			name:  "neither role nor permission",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2"},
			want:  ErrInvalidRole,
		},
		{
			name:  "role and permission together",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Role: ProjectViewer, Permission: View},
			want:  ErrInvalidRole,
		},
		{
			name:  "role only",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Role: ProjectViewer},
			want:  nil,
		},
		{
			name:  "permission only",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: View},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := service.GrantAccess(context.Background(), actor, tc.grant)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("GrantAccess returned %v, want success", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("GrantAccess error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGrantAccessRejectsRoleToRoleGrant(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}
	service := New(repo, true)
	err := service.GrantAccess(context.Background(), Subject{UserID: "owner-1", WorkspaceID: "ws-1"}, AccessGrant{
		ProjectID:   "p-1",
		SubjectType: SubjectRole,
		SubjectID:   string(ProjectViewer),
		Role:        ProjectMember,
	})
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("role-to-role grant error = %v, want %v", err, ErrInvalidRole)
	}
}

func TestRevokeAccessValidatesGrantShapeBeforeDelete(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}
	service := New(repo, true)
	actor := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	cases := []struct {
		name  string
		grant AccessGrant
		want  error
	}{
		{
			name:  "unknown subject type",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectType("team"), SubjectID: "team-1", Permission: View},
			want:  ErrInvalidRole,
		},
		{
			name:  "empty subject id",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, Permission: View},
			want:  ErrInvalidSubject,
		},
		{
			name:  "unknown permission",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: Permission("project.unknown")},
			want:  ErrInvalidIssuePermission,
		},
		{
			name:  "role and permission together",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Role: ProjectMember, Permission: View},
			want:  ErrInvalidRole,
		},
		{
			name:  "role subject with role grant",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectRole, SubjectID: string(ProjectMember), Role: ProjectViewer},
			want:  ErrInvalidRole,
		},
		{
			name:  "unknown role subject",
			grant: AccessGrant{ProjectID: "p-1", SubjectType: SubjectRole, SubjectID: "not-a-role", Permission: View},
			want:  ErrInvalidSubject,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := service.RevokeAccess(context.Background(), actor, tc.grant); !errors.Is(err, tc.want) {
				t.Fatalf("RevokeAccess error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTaskRoleGrantIsTaskScoped(t *testing.T) {
	repo := &fakeGrantRepo{
		fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1", issueProject: "p-1"},
		grants: []AccessGrant{
			{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Permission: View},
			{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-1", Role: ProjectViewer},
			{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectRole, SubjectID: string(ProjectViewer), Permission: Edit},
		},
	}
	service := New(repo, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	if err := service.CheckIssue(context.Background(), subject, "i-1", "p-1", Edit); err != nil {
		t.Fatalf("task role grant should allow the assigned task: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "i-2", "p-1", Edit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("task role grant leaked to another task: %v", err)
	}
}

func TestTaskRoleSubjectUsesProjectRoleAndStaysTaskScoped(t *testing.T) {
	repo := &fakeGrantRepo{
		fakeRepo: fakeRepo{workspace: string(WorkspaceMember), projectWorkspace: "ws-1", issueProject: "p-1"},
		grants: []AccessGrant{
			// The caller is a Member on the project, so the task's minimum
			// visibility check succeeds through project-role inheritance.
			{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-1", Role: ProjectMember},
			// This extra permission is granted to project Members, but only on i-1.
			{ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectRole, SubjectID: string(ProjectMember), Permission: Edit},
		},
	}
	service := New(repo, true)
	subject := Subject{UserID: "u-1", WorkspaceID: "ws-1"}
	if err := service.CheckIssue(context.Background(), subject, "i-1", "p-1", Edit); err != nil {
		t.Fatalf("task role subject should match the caller's project role: %v", err)
	}
	if err := service.CheckIssue(context.Background(), subject, "i-2", "p-1", Edit); !errors.Is(err, ErrForbidden) {
		t.Fatalf("task role subject leaked to another task: %v", err)
	}
}

func TestLegacyGrantAdapterWithoutAuditRemainsCompatible(t *testing.T) {
	repo := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}
	service := New(repo, true)
	actor := Subject{UserID: "owner-1", WorkspaceID: "ws-1"}
	grant := AccessGrant{ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: View}
	if err := service.GrantAccess(context.Background(), actor, grant); err != nil {
		t.Fatalf("legacy adapter GrantAccess: %v", err)
	}
	if err := service.RevokeAccess(context.Background(), actor, grant); err != nil {
		t.Fatalf("legacy adapter RevokeAccess: %v", err)
	}
}

func TestGrantAccessFailsClosedWithoutSubjectRepository(t *testing.T) {
	base := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}
	repo := &subjectlessGrantRepo{GrantRepository: base}
	err := New(repo, true).GrantAccess(context.Background(), Subject{UserID: "owner-1", WorkspaceID: "ws-1"}, AccessGrant{
		ProjectID: "p-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: View,
	})
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("GrantAccess without subject repository = %v, want %v", err, ErrMigrationRequired)
	}
}

func TestTaskGrantFailsClosedWithoutResourceRepository(t *testing.T) {
	base := &fakeGrantRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner), projectWorkspace: "ws-1"}}
	repo := &subjectlessGrantRepo{GrantRepository: base}
	err := New(repo, true).GrantAccess(context.Background(), Subject{UserID: "owner-1", WorkspaceID: "ws-1"}, AccessGrant{
		ProjectID: "p-1", IssueID: "i-1", SubjectType: SubjectUser, SubjectID: "u-2", Permission: View,
	})
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("task GrantAccess without resource repository = %v, want %v", err, ErrMigrationRequired)
	}
}
