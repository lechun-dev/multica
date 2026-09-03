package projectauth

import (
	"context"
	"errors"
	"fmt"
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
	// 2026-09-01 coder(lq): Resolve the canonical task binding before any
	// permission lookup. Callers must not pair an issue from one project or
	// workspace with another project ID supplied in the request.
	resourceRepo, ok := s.repo.(ResourceRepository)
	if !ok {
		// 2026-09-01 coder(lq): An enabled task ACL must resolve the canonical
		// issue binding before checking grants; otherwise a caller could pair a
		// valid project grant with an unrelated issue ID.
		return ErrMigrationRequired
	}
	workspaceID, boundProjectID, err := resourceRepo.IssueProject(ctx, issueID)
	if err != nil {
		if errors.Is(err, ErrNoProjectAccess) || errors.Is(err, ErrCrossWorkspace) {
			return err
		}
		return authorizationStorageError(err)
	}
	if workspaceID != subject.WorkspaceID || boundProjectID != projectID {
		return ErrNoProjectAccess
	}
	// A task grant can only add capabilities after the caller has the
	// project's minimum visibility. This prevents assignee/@ grants from
	// exposing an otherwise unrelated project or its other tasks.
	if err := s.CheckWithWorkspaceScope(ctx, subject, projectID, View, includeWorkspaceOwned); err != nil {
		return err
	}
	if err := s.CheckWithWorkspaceScope(ctx, subject, projectID, permission, includeWorkspaceOwned); err == nil {
		return nil
	} else if !errors.Is(err, ErrForbidden) {
		// 2026-09-01 coder(lq): A task grant may supplement a denied project
		// operation, but it must never mask storage, migration, membership, or
		// resource errors. Falling through on those errors would turn an ACL
		// outage into a task-level fail-open path.
		return err
	}
	if grants, ok := s.repo.(GrantRepository); ok {
		allowed, _, grantErr := s.checkGrants(ctx, grants, subject, projectID, issueID, permission)
		if grantErr != nil {
			return grantErr
		}
		if allowed {
			return nil
		}
	} else {
		return ErrMigrationRequired
	}
	return fmt.Errorf("%w: permission=%s", ErrForbidden, permission)
}
