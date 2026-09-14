package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestIssueAccessRequestLifecycleIsScopedIdempotentAndAtomic(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Access request", "access-request")
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	requester := dbfx.User(t, "Access requester", "access-requester@example.test")
	manager := dbfx.User(t, "Task manager", "task-manager@example.test")
	fx.Member(t, ws, requester, "member")
	fx.Member(t, ws, manager, "member")
	project := fx.Project(t, "Access request project")
	issue := fx.Issue(t, "Restricted request task", testutil.Cols{"project_id": project})
	fx.InsertNoID(t, "projectauth_issue_policies", testutil.Cols{"workspace_id": ws, "issue_id": issue, "project_access_mode": "restricted", "policy_version": 1}, "workspace_id=$1 AND issue_id=$2", ws, issue)
	fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": ws, "project_id": project, "issue_id": issue, "subject_type": "user", "subject_id": manager, "role_key": "manager", "source": "manual"})

	requesterSubject := projectauth.Subject{UserID: requester, WorkspaceID: ws, WorkspaceRole: projectauth.WorkspaceMember}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	create := issueAccessRequestCreate{RequestedRole: projectauth.RoleKey(projectauth.TaskMember), Reason: "需要协作", ExpiresAt: &expires, IdempotencyKey: "request-lifecycle"}
	tx, _ := testPool.Begin(ctx)
	item, created, err := testHandler.createIssueAccessRequest(ctx, tx, requesterSubject, issue, create)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if !created || item.Status != accessRequestPending {
		t.Fatalf("created=%v item=%#v", created, item)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Transport retry returns the original unit and does not duplicate inbox or audit.
	tx, _ = testPool.Begin(ctx)
	retried, created, err := testHandler.createIssueAccessRequest(ctx, tx, requesterSubject, issue, create)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if created || retried.ID != item.ID {
		t.Fatalf("retry created=%v item=%#v", created, retried)
	}
	_ = tx.Rollback(ctx)
	var requests, ownerInbox, createdAudits int
	fx.QueryRow(t, `SELECT count(*) FROM projectauth_access_requests WHERE workspace_id=$1 AND issue_id=$2`, ws, issue).Scan(&requests)
	fx.QueryRow(t, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND issue_id=$2 AND type='task_access_request' AND recipient_id=$3`, ws, issue, testUserID).Scan(&ownerInbox)
	fx.QueryRow(t, `SELECT count(*) FROM activity_log WHERE workspace_id=$1 AND issue_id=$2 AND action='task_access_request_created'`, ws, issue).Scan(&createdAudits)
	if requests != 1 || ownerInbox != 1 || createdAudits != 1 {
		t.Fatalf("requests/inbox/audits=%d/%d/%d", requests, ownerInbox, createdAudits)
	}

	// Manager has IssueManage but is not an effective Owner.
	managerSubject := projectauth.Subject{UserID: manager, WorkspaceID: ws, WorkspaceRole: projectauth.WorkspaceMember}
	tx, _ = testPool.Begin(ctx)
	_, err = testHandler.reviewIssueAccessRequest(ctx, tx, managerSubject, issue, item.ID, issueAccessRequestReview{Action: "approve"})
	_ = tx.Rollback(ctx)
	if !errors.Is(err, projectauth.ErrForbidden) {
		t.Fatalf("manager review error=%v, want forbidden", err)
	}

	ownerSubject := projectauth.Subject{UserID: testUserID, WorkspaceID: ws, WorkspaceRole: projectauth.WorkspaceOwner}
	tx, _ = testPool.Begin(ctx)
	approved, err := testHandler.reviewIssueAccessRequest(ctx, tx, ownerSubject, issue, item.ID, issueAccessRequestReview{Action: "approve", Comment: "同意"})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if approved.Status != accessRequestApproved {
		t.Fatalf("status=%s", approved.Status)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var grants, constraints, grantAudits, resultInbox int
	fx.QueryRow(t, `SELECT count(*) FROM projectauth_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3 AND role_key='member'`, ws, issue, requester).Scan(&grants)
	fx.QueryRow(t, `SELECT count(*) FROM projectauth_grant_constraints c JOIN projectauth_access_grants g ON g.id=c.grant_id WHERE c.workspace_id=$1 AND g.issue_id=$2 AND c.origin_kind='access_request' AND c.origin_id=$3`, ws, issue, item.ID).Scan(&constraints)
	fx.QueryRow(t, `SELECT count(*) FROM activity_log WHERE workspace_id=$1 AND issue_id=$2 AND action='task_access_grant_created_from_request'`, ws, issue).Scan(&grantAudits)
	fx.QueryRow(t, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND issue_id=$2 AND type='task_access_request' AND recipient_id=$3`, ws, issue, requester).Scan(&resultInbox)
	if grants != 1 || constraints != 1 || grantAudits != 1 || resultInbox != 1 {
		t.Fatalf("grant/constraint/audit/result inbox=%d/%d/%d/%d", grants, constraints, grantAudits, resultInbox)
	}

	// Repeating the same terminal transition is a no-op; the opposite one conflicts.
	tx, _ = testPool.Begin(ctx)
	_, err = testHandler.reviewIssueAccessRequest(ctx, tx, ownerSubject, issue, item.ID, issueAccessRequestReview{Action: "approve"})
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("idempotent approve: %v", err)
	}
	tx, _ = testPool.Begin(ctx)
	_, err = testHandler.reviewIssueAccessRequest(ctx, tx, ownerSubject, issue, item.ID, issueAccessRequestReview{Action: "reject"})
	_ = tx.Rollback(ctx)
	var conflict accessRequestStateConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("opposite review error=%v, want conflict", err)
	}
}

func TestIssueAccessRequestExpiryAndRequesterCancellation(t *testing.T) {
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Access request terminal", "access-request-terminal")
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	requester := dbfx.User(t, "Terminal requester", "terminal-requester@example.test")
	fx.Member(t, ws, requester, "member")
	issue := fx.Issue(t, "Terminal access request")
	expired := time.Now().Add(-time.Minute)
	requestID := fx.Insert(t, "projectauth_access_requests", testutil.Cols{"workspace_id": ws, "issue_id": issue, "requester_user_id": requester, "requested_role_key": "viewer", "idempotency_key": "expired", "created_at": time.Now().Add(-2 * time.Minute), "grant_expires_at": expired})
	owner := projectauth.Subject{UserID: testUserID, WorkspaceID: ws, WorkspaceRole: projectauth.WorkspaceOwner}
	tx, _ := testPool.Begin(ctx)
	_, err := testHandler.reviewIssueAccessRequest(ctx, tx, owner, issue, requestID, issueAccessRequestReview{Action: "approve"})
	_ = tx.Rollback(ctx)
	var conflict accessRequestStateConflict
	if !errors.As(err, &conflict) || conflict.status != accessRequestExpired {
		t.Fatalf("expired review error=%v", err)
	}
}
