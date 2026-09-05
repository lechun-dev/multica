package projectauth

import (
	"context"
)

// 2026-09-05 coder(lq): A user mentioned on a task is a task member, not a
// project member. Task grants are intentionally limited to viewing the task
// and participating in its conversation; every other operation still comes
// from the parent project's role.
func (s *Service) CheckIssue(ctx context.Context, subject Subject, issueID, projectID string, permission Permission) error {
	return s.CheckIssueWithWorkspaceScope(ctx, subject, issueID, projectID, permission, true)
}

// 2026-09-01 coder(lq): Keep direct issue checks aligned with list visibility
// so a restricted workspace-owner request cannot open a hidden task by URL.
func (s *Service) CheckIssueWithWorkspaceScope(ctx context.Context, subject Subject, issueID, projectID string, permission Permission, includeWorkspaceOwned bool) error {
	if s == nil || !s.enabled {
		return nil
	}
	if s.repo == nil {
		return ErrDisabled
	}
	if issueID == "" || projectID == "" {
		return ErrNoProjectAccess
	}
	// Resolve workspace membership before looking at the task grant. This keeps
	// stale issue_permissions rows harmless after a user leaves the workspace.
	if _, err := s.repo.WorkspaceRole(ctx, subject.WorkspaceID, subject.UserID); err != nil {
		return ErrNotWorkspaceMember
	}
	if permission == View || permission == IssueComment {
		if reader, ok := s.repo.(IssuePermissionReader); ok {
			allowed, err := reader.IssuePermission(ctx, issueID, subject.UserID, permission)
			if err == nil && allowed {
				return nil
			}
		}
	}
	return s.CheckWithWorkspaceScope(ctx, subject, projectID, permission, includeWorkspaceOwned)
}
