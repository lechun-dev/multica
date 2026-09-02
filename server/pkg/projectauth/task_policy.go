package projectauth

import (
	"context"
)

// 2026-08-27 coder(lq): Tasks do not have an independent authorization scope.
// Keep the issue ID validation here, then delegate the requested operation to
// the parent project. A task therefore inherits the project's full permission
// matrix (View, Edit, IssueCreate, IssueComment, IssueManage, AgentUse, and administrative
// permissions), rather than receiving a View-only grant. Legacy
// issue_permissions rows can never grant access.
func (s *Service) CheckIssue(ctx context.Context, subject Subject, issueID, projectID string, permission Permission) error {
	return s.CheckIssueWithWorkspaceScope(ctx, subject, issueID, projectID, permission, true)
}

// 2026-09-01 coder(lq): Keep direct issue checks aligned with list visibility
// so a restricted workspace-owner request cannot open a hidden task by URL.
func (s *Service) CheckIssueWithWorkspaceScope(ctx context.Context, subject Subject, issueID, projectID string, permission Permission, includeWorkspaceOwned bool) error {
	if s == nil || !s.enabled {
		return nil
	}
	if issueID == "" || projectID == "" {
		return ErrNoProjectAccess
	}
	return s.CheckWithWorkspaceScope(ctx, subject, projectID, permission, includeWorkspaceOwned)
}
