package projectauth

import (
	"context"
)

// 2026-08-27 coder(lq): Tasks do not have an independent authorization scope.
// Keep the issue ID validation here, then delegate the requested operation to
// the parent project. A task therefore inherits the project's full permission
// matrix (View, Edit, IssueCreate, IssueManage, AgentUse, and administrative
// permissions), rather than receiving a View-only grant. Legacy
// issue_permissions rows can never grant access.
func (s *Service) CheckIssue(ctx context.Context, subject Subject, issueID, projectID string, permission Permission) error {
	if s == nil || !s.enabled {
		return nil
	}
	if issueID == "" || projectID == "" {
		return ErrNoProjectAccess
	}
	return s.Check(ctx, subject, projectID, permission)
}
