package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestApplyIssueAccessControlIsAtomicVersionedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Atomic task ACL", "atomic-task-acl")
	fx := testutil.New(testPool, ws, testUserID)
	member := dbfx.User(t, "ACL member", "acl-member@example.test")
	fx.Member(t, ws, member, "member")
	project := fx.Project(t, "ACL project")
	issue := fx.Issue(t, "ACL task", testutil.Cols{"project_id": project})
	actor := projectauth.Subject{UserID: testUserID, WorkspaceID: ws, WorkspaceRole: projectauth.WorkspaceOwner}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	request := issueAccessControlRequest{ExpectedVersion: 1, ProjectAccessMode: projectauth.ProjectAccessRestricted,
		Grants: []issueAccessControlGrant{{SubjectType: projectauth.SubjectUser, SubjectID: member, Role: projectauth.RoleKey(projectauth.TaskMember), Scope: projectauth.RoleScopeTask, ExpiresAt: &expires}}}

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, changed, err := testHandler.applyIssueAccessControl(ctx, tx, actor, issue, project, request)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if !changed || state.PolicyVersion != 2 || state.ProjectAccessMode != projectauth.ProjectAccessRestricted {
		t.Fatalf("unexpected state: %#v", state)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var grants, audits int
	fx.QueryRow(t, `SELECT count(*) FROM projectauth_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND source='manual'`, ws, issue).Scan(&grants)
	fx.QueryRow(t, `SELECT count(*) FROM activity_log WHERE workspace_id=$1 AND issue_id=$2 AND action='task_access_control_updated'`, ws, issue).Scan(&audits)
	if grants != 1 || audits != 1 {
		t.Fatalf("grants=%d audits=%d, want 1/1", grants, audits)
	}

	// A transport retry with the same desired state is a no-op even though its
	// expected version is stale; it neither duplicates the row nor the audit.
	tx, _ = testPool.Begin(ctx)
	state, changed, err = testHandler.applyIssueAccessControl(ctx, tx, actor, issue, project, request)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if changed || state.PolicyVersion != 2 {
		t.Fatalf("idempotent retry changed state: %#v", state)
	}
	_ = tx.Rollback(ctx)
	fx.QueryRow(t, `SELECT count(*) FROM activity_log WHERE workspace_id=$1 AND issue_id=$2 AND action='task_access_control_updated'`, ws, issue).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit count=%d, want 1", audits)
	}

	// A stale request for a different state gets a version conflict.
	request.ProjectAccessMode = projectauth.ProjectAccessInherit
	tx, _ = testPool.Begin(ctx)
	_, _, err = testHandler.applyIssueAccessControl(ctx, tx, actor, issue, project, request)
	_ = tx.Rollback(ctx)
	var conflict issuePolicyVersionConflict
	if !errors.As(err, &conflict) || conflict.current != 2 {
		t.Fatalf("error=%v, want version conflict at 2", err)
	}

	// Validation happens before destructive replacement; an invalid subject
	// leaves both the policy and the existing ACL intact.
	request.ExpectedVersion = 2
	request.Grants[0].SubjectID = "not-a-uuid"
	tx, _ = testPool.Begin(ctx)
	_, _, err = testHandler.applyIssueAccessControl(ctx, tx, actor, issue, project, request)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, projectauth.ErrInvalidSubject) {
		t.Fatalf("error=%v, want invalid subject", err)
	}
	fx.QueryRow(t, `SELECT count(*) FROM projectauth_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND source='manual'`, ws, issue).Scan(&grants)
	if grants != 1 {
		t.Fatalf("grant count after rejected update=%d, want 1", grants)
	}
}

func TestIssueAccessControlRejectsWrongScopeAndExpiredGrant(t *testing.T) {
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Scoped task ACL", "scoped-task-acl")
	fx := testutil.New(testPool, ws, testUserID)
	member := dbfx.User(t, "Scoped member", "scoped-member@example.test")
	fx.Member(t, ws, member, "member")

	for name, grant := range map[string]issueAccessControlGrant{
		"project role scope": {SubjectType: projectauth.SubjectUser, SubjectID: member, Role: "member", Scope: projectauth.RoleScopeProject},
		"expired":            {SubjectType: projectauth.SubjectUser, SubjectID: member, Role: "member", Scope: projectauth.RoleScopeTask, ExpiresAt: ptrTime(time.Now().Add(-time.Minute))},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateIssueAccessControlGrants(ctx, testPool, ws, []issueAccessControlGrant{grant})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
