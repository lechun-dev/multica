package projectauth

import (
	"context"
	"fmt"
)

// 2026-08-27 coder(lq): Permission reports expose effective workspace/project
// authorization only. Tasks inherit these project permissions and therefore
// are intentionally absent as an independent report scope.
type PermissionReportFilter struct {
	WorkspaceID string
	ProjectID   string
	UserID      string
	Role        string
	Permission  Permission
	Scope       string // all or project
	Limit       int
	Offset      int
}

type PermissionReportRow struct {
	Scope         string        `json:"scope"`
	ProjectID     string        `json:"project_id"`
	ProjectTitle  string        `json:"project_title"`
	UserID        string        `json:"user_id"`
	UserName      string        `json:"user_name"`
	UserEmail     string        `json:"user_email"`
	WorkspaceRole WorkspaceRole `json:"workspace_role"`
	ProjectRole   ProjectRole   `json:"project_role,omitempty"`
	Permission    Permission    `json:"permission"`
	Source        string        `json:"source"` // workspace_role, project_role
	GrantedBy     string        `json:"granted_by,omitempty"`
}

type PermissionReportResult struct {
	Rows  []PermissionReportRow
	Total int64
}

type PermissionReportRepository interface {
	Repository
	ListPermissionReport(ctx context.Context, filter PermissionReportFilter) (PermissionReportResult, error)
}

// 2026-08-27 coder(lq): Reports are administrative reads. Only workspace
// owners may report across the workspace; other users need project settings
// permission and must scope the report to one project.
func (s *Service) ListPermissionReport(ctx context.Context, subject Subject, filter PermissionReportFilter) (PermissionReportResult, error) {
	if s == nil || !s.enabled {
		return PermissionReportResult{}, nil
	}
	if s.repo == nil {
		return PermissionReportResult{}, ErrDisabled
	}
	rr, ok := s.repo.(PermissionReportRepository)
	if !ok {
		return PermissionReportResult{}, ErrDisabled
	}
	if filter.WorkspaceID == "" {
		filter.WorkspaceID = subject.WorkspaceID
	}
	if filter.WorkspaceID == "" || filter.WorkspaceID != subject.WorkspaceID {
		return PermissionReportResult{}, ErrCrossWorkspace
	}
	if filter.Scope == "" {
		filter.Scope = "all"
	}
	if filter.Scope != "all" && filter.Scope != "project" {
		return PermissionReportResult{}, fmt.Errorf("%w: scope=%s", ErrInvalidReportFilter, filter.Scope)
	}
	if filter.Role != "" && !validReportRole(filter.Role) {
		return PermissionReportResult{}, fmt.Errorf("%w: role=%s", ErrInvalidReportFilter, filter.Role)
	}
	if filter.Permission != "" && !validReportPermission(filter.Permission) {
		return PermissionReportResult{}, fmt.Errorf("%w: permission=%s", ErrInvalidReportFilter, filter.Permission)
	}
	if filter.Limit <= 0 || filter.Limit > 1000 {
		filter.Limit = 1000
	}
	if filter.Offset < 0 {
		return PermissionReportResult{}, fmt.Errorf("%w: offset=%d", ErrInvalidReportFilter, filter.Offset)
	}
	role, err := s.repo.WorkspaceRole(ctx, subject.WorkspaceID, subject.UserID)
	if err != nil {
		return PermissionReportResult{}, ErrNotWorkspaceMember
	}
	if role != WorkspaceOwner {
		if filter.ProjectID == "" {
			return PermissionReportResult{}, ErrForbidden
		}
		if err := s.Check(ctx, subject, filter.ProjectID, SettingsManage); err != nil {
			return PermissionReportResult{}, err
		}
	}
	return rr.ListPermissionReport(ctx, filter)
}

func validReportRole(role string) bool {
	switch role {
	// 2026-08-27 coder(lq): Workspace and project roles intentionally share
	// owner/member values, so validate their distinct wire values only once.
	case "owner", "admin", "member", "manager", "viewer":
		return true
	default:
		return false
	}
}

func validReportPermission(permission Permission) bool {
	switch permission {
	case View, Edit, IssueCreate, IssueManage, AgentUse, MemberManage, SettingsManage:
		return true
	default:
		return false
	}
}
