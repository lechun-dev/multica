package projectauth

import (
	"context"
	"errors"
	"testing"
)

type fakeReportRepo struct {
	fakeRepo
	lastFilter PermissionReportFilter
}

func (f *fakeReportRepo) ListPermissionReport(_ context.Context, filter PermissionReportFilter) (PermissionReportResult, error) {
	f.lastFilter = filter
	return PermissionReportResult{Total: 1}, nil
}

func reportSubject(role WorkspaceRole) Subject {
	return Subject{UserID: "u-1", WorkspaceID: "ws-1", WorkspaceRole: role}
}

func TestPermissionReportWorkspaceAdminMustScopeProject(t *testing.T) {
	repo := &fakeReportRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceAdmin)}}
	s := New(repo, true)
	if _, err := s.ListPermissionReport(context.Background(), reportSubject(WorkspaceAdmin), PermissionReportFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want %v", err, ErrForbidden)
	}
}

func TestPermissionReportWorkspaceOwnerCanQueryWorkspace(t *testing.T) {
	repo := &fakeReportRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceOwner)}}
	s := New(repo, true)
	if _, err := s.ListPermissionReport(context.Background(), reportSubject(WorkspaceOwner), PermissionReportFilter{}); err != nil {
		t.Fatalf("owner report: %v", err)
	}
	if repo.lastFilter.WorkspaceID != "ws-1" || repo.lastFilter.Limit != 1000 {
		t.Fatalf("unexpected normalized filter: %+v", repo.lastFilter)
	}
}

func TestPermissionReportMemberMustScopeProject(t *testing.T) {
	repo := &fakeReportRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), project: string(ProjectOwner), projectWorkspace: "ws-1"}}
	s := New(repo, true)
	_, err := s.ListPermissionReport(context.Background(), reportSubject(WorkspaceMember), PermissionReportFilter{})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want %v", err, ErrForbidden)
	}
}

func TestPermissionReportMemberNeedsSettingsManage(t *testing.T) {
	repo := &fakeReportRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceMember), project: string(ProjectManager), projectWorkspace: "ws-1"}}
	s := New(repo, true)
	_, err := s.ListPermissionReport(context.Background(), reportSubject(WorkspaceMember), PermissionReportFilter{ProjectID: "p-1"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want %v", err, ErrForbidden)
	}
}

func TestPermissionReportProjectOwnerCanQueryProject(t *testing.T) {
	repo := &fakeReportRepo{fakeRepo: fakeRepo{
		workspace: string(WorkspaceMember), project: string(ProjectOwner), projectWorkspace: "ws-1",
	}}
	s := New(repo, true)
	if _, err := s.ListPermissionReport(context.Background(), reportSubject(WorkspaceMember), PermissionReportFilter{ProjectID: "p-1", Scope: "project"}); err != nil {
		t.Fatalf("project-scoped report: %v", err)
	}
	if repo.lastFilter.ProjectID != "p-1" || repo.lastFilter.Scope != "project" {
		t.Fatalf("unexpected repository filter: %+v", repo.lastFilter)
	}
}

func TestPermissionReportRejectsCrossWorkspaceAndInvalidFilters(t *testing.T) {
	repo := &fakeReportRepo{fakeRepo: fakeRepo{workspace: string(WorkspaceAdmin)}}
	s := New(repo, true)
	for name, filter := range map[string]PermissionReportFilter{
		"cross workspace":    {WorkspaceID: "ws-2"},
		"invalid scope":      {Scope: "user"},
		"invalid subject":    {SubjectType: "team"},
		"invalid role":       {Role: "unknown"},
		"invalid permission": {Permission: "project.delete"},
		"negative offset":    {Offset: -1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.ListPermissionReport(context.Background(), reportSubject(WorkspaceAdmin), filter)
			if !errors.Is(err, ErrCrossWorkspace) && !errors.Is(err, ErrInvalidReportFilter) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
