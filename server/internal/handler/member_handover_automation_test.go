package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func verifyHandoverEvidence(t *testing.T, ws string, verified, current []string) {
	t.Helper()
	ctx := context.Background()
	tx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = testHandler.RecordDingTalkHandoverEvidence(ctx, tx, ws, verified, current); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestAutomaticHandoverRequiresEvidenceAndCatchesHistoricalDepartures(t *testing.T) {
	fx, ws, source, _, recipient := handoverFixture(t)
	issue := fx.Issue(t, "Historical work", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
	project := fx.Project(t, "Historical project", testutil.Cols{"created_by": source, "lead_type": "member", "lead_id": source})
	ctx := context.Background()
	if result, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil || result.Completed != 0 {
		t.Fatalf("manual disable qualified: %+v %v", result, err)
	}
	verifyHandoverEvidence(t, ws, []string{source}, nil)
	result, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID)
	if err != nil || result.Completed != 1 {
		t.Fatalf("catch-up: %+v %v", result, err)
	}
	if fx.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND assignee_id=$2 AND creator_id=$3`, issue, source, source) != 1 {
		t.Fatal("assignment/history incorrect")
	}
	if fx.Count(t, `SELECT count(*) FROM project WHERE id=$1 AND lead_id=$2 AND created_by=$3`, project, source, source) != 1 {
		t.Fatal("project/history incorrect")
	}
	if fx.Count(t, `SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3`, ws, issue, recipient) != 1 {
		t.Fatal("leader did not receive task permission")
	}
	if fx.Count(t, `SELECT count(*) FROM member_handover_resources WHERE workspace_id=$1`, ws) != 2 {
		t.Fatal("missing resource ledger")
	}
	if fx.Count(t, `SELECT count(*) FROM member_handovers WHERE workspace_id=$1 AND automatic AND completed_at IS NOT NULL`, ws) != 1 {
		t.Fatal("automatic execution was not distinguished in the audit record")
	}
	if fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND status='pending'`, ws) != 1 {
		t.Fatal("checklist summary intent not atomic")
	}
	var payload string
	fx.QueryRow(t, `SELECT payload::text FROM member_handover_notifications WHERE workspace_id=$1 AND issue_id=(SELECT checklist_issue_id FROM member_handovers WHERE workspace_id=$1 AND completed_at IS NOT NULL LIMIT 1)`, ws).Scan(&payload)
	if !strings.Contains(payload, "离职工作权限已开放") {
		t.Fatal("automatic notification impersonates a human")
	}
	other := fx.User(t, "New manager", ws+"new@example.test")
	fx.Member(t, ws, other, "member")
	fx.Exec(t, `UPDATE directory_reporting_lines SET manager_external_id='new-manager' WHERE workspace_id=$1 AND user_id=$2`, ws, source)
	fx.InsertNoID(t, "directory_reporting_lines", testutil.Cols{"workspace_id": ws, "user_id": other, "provider": "dingtalk", "external_user_id": "new-manager", "sync_status": "complete", "successful_at": time.Now()}, "workspace_id=$1 AND user_id=$2", ws, other)
	for range 2 {
		if _, err = testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
			t.Fatal(err)
		}
	}
	if fx.Count(t, `SELECT count(*) FROM member_handovers WHERE workspace_id=$1 AND completed_at IS NOT NULL`, ws) != 1 || fx.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND assignee_id=$2`, issue, source) != 1 {
		t.Fatal("repeat sync or manager change retransferred completed work")
	}
}
func TestAutomaticHandoverPendingDoesNotBlockOtherMembers(t *testing.T) {
	fx, ws, source, _, recipient := handoverFixture(t)
	ctx := context.Background()
	fx.Issue(t, "Pending work", testutil.Cols{"creator_id": source})
	fx.Exec(t, `UPDATE directory_reporting_lines SET manager_external_id='unlinked' WHERE workspace_id=$1 AND user_id=$2`, ws, source)
	second := fx.User(t, "Second departed", ws+"second@example.test", testutil.Cols{"account_disabled_at": time.Now()})
	fx.Member(t, ws, second, "member")
	secondIssue := fx.Issue(t, "Ready work", testutil.Cols{"creator_id": second, "assignee_type": "member", "assignee_id": second})
	fx.InsertNoID(t, "directory_reporting_lines", testutil.Cols{"workspace_id": ws, "user_id": second, "provider": "dingtalk", "external_user_id": "second", "manager_external_id": "0201243712852502", "sync_status": "complete", "successful_at": time.Now()}, "workspace_id=$1 AND user_id=$2", ws, second)
	verifyHandoverEvidence(t, ws, []string{source, second}, nil)
	result, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID)
	if err != nil || result.Pending != 1 || result.Completed != 1 {
		t.Fatalf("isolated result: %+v %v", result, err)
	}
	if fx.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND assignee_id=$2`, secondIssue, second) != 1 {
		t.Fatal("pending member blocked ready member")
	}
	if _, err = testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
		t.Fatal(err)
	}
	if fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND event_id LIKE 'handover-problem:%'`, ws) != 0 {
		t.Fatal("exception unexpectedly notified an owner")
	}
	fx.Exec(t, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, recipient)
	fx.Issue(t, "New outstanding work", testutil.Cols{"creator_id": second})
	result, err = testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID)
	if err != nil || result.Pending != 2 {
		t.Fatalf("departed manager should be pending: %+v %v", result, err)
	}
	verifyHandoverEvidence(t, ws, nil, []string{source, second})
	result, err = testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID)
	if err != nil || result.Pending != 0 {
		t.Fatalf("returned employee retained evidence: %+v %v", result, err)
	}
}
func TestAutomaticAndManualHandoverCannotDoubleTransfer(t *testing.T) {
	fx, ws, source, member, recipient := handoverFixture(t)
	issue := fx.Issue(t, "Concurrent work", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
	verifyHandoverEvidence(t, ws, []string{source}, nil)
	var preview memberHandoverPreview
	handoverRequest(t, testUserID, ws, member, false, map[string]any{}).Want(200).JSON(&preview)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		handoverRequest(t, testUserID, ws, member, true, map[string]any{"preview_id": preview.PreviewID}).WantOneOf(200, 409)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = testHandler.RunAutomaticMemberHandovers(context.Background(), ws, testUserID)
	}()
	close(start)
	wg.Wait()
	if _, err := testHandler.RunAutomaticMemberHandovers(context.Background(), ws, testUserID); err != nil {
		t.Fatal(err)
	}
	if fx.Count(t, `SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3`, ws, issue, recipient) != 1 {
		t.Fatal("leader did not receive task permission")
	}
	if fx.Count(t, `SELECT count(*) FROM member_handover_resources WHERE workspace_id=$1`, ws) != 1 || fx.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND assignee_id=$2`, issue, source) != 1 {
		t.Fatal("concurrent transfer duplicated or lost work")
	}
	if fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1`, ws) != 1 {
		t.Fatal("concurrent handover duplicated notification intent")
	}
}

// Fail only the first candidate's diagnostic write, without installing database
// triggers or altering shared schema. All remaining operations use real fixture transactions.
type failingHandoverProblemStarter struct {
	txStarter
	source string
}

func (s failingHandoverProblemStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failingHandoverProblemTx{Tx: tx, source: s.source}, nil
}

type failingHandoverProblemTx struct {
	pgx.Tx
	source string
}

func (tx failingHandoverProblemTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "SET status=$3,reason=$4") && len(args) > 1 && args[1] == tx.source {
		return pgconn.CommandTag{}, errors.New("fixture diagnostic persistence failure")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestAutomaticHandoverStatusWriteFailureDoesNotStopLaterMembers(t *testing.T) {
	fx, ws, source, _, recipient := handoverFixture(t)
	second := fx.User(t, "Second departed", ws+"-failure-isolation@example.test", testutil.Cols{"account_disabled_at": time.Now()})
	fx.Member(t, ws, second, "member")
	fx.InsertNoID(t, "directory_reporting_lines", testutil.Cols{"workspace_id": ws, "user_id": second, "provider": "dingtalk", "external_user_id": "second", "manager_external_id": "0201243712852502", "sync_status": "complete", "successful_at": time.Now()}, "workspace_id=$1 AND user_id=$2", ws, second)
	first, later := source, second
	if first > later {
		first, later = later, first
	}
	fx.Exec(t, `UPDATE directory_reporting_lines SET manager_external_id='unlinked' WHERE workspace_id=$1 AND user_id=$2`, ws, first)
	fx.Issue(t, "Cannot record pending status", testutil.Cols{"creator_id": first})
	issue := fx.Issue(t, "Later member work", testutil.Cols{"creator_id": later, "assignee_type": "member", "assignee_id": later})
	verifyHandoverEvidence(t, ws, []string{first, later}, nil)
	h := *testHandler
	h.TxStarter = failingHandoverProblemStarter{txStarter: h.TxStarter, source: first}
	result, err := h.RunAutomaticMemberHandovers(context.Background(), ws, testUserID)
	if err == nil || result.Failed != 1 || result.Completed != 1 {
		t.Fatalf("diagnostic failure blocked remaining candidates: %+v %v", result, err)
	}
	if fx.Count(t, `SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3`, ws, issue, recipient) != 1 {
		t.Fatal("later candidate permission missing")
	}
	if fx.Count(t, `SELECT count(*) FROM issue WHERE id=$1 AND assignee_id=$2`, issue, later) != 1 {
		t.Fatal("later candidate's work was not transferred")
	}
}

func TestHandoverNotificationRoutingRecoversWithoutRepeatingBusinessWork(t *testing.T) {
	fx, ws, source, _, recipient := handoverFixture(t)
	ctx := context.Background()
	_ = fx.Issue(t, "Notification work", testutil.Cols{"creator_id": source, "assignee_type": "member", "assignee_id": source})
	verifyHandoverEvidence(t, ws, []string{source}, nil)
	if _, err := testHandler.RunAutomaticMemberHandovers(ctx, ws, testUserID); err != nil {
		t.Fatal(err)
	}
	var checklist string
	fx.QueryRow(t, `SELECT checklist_issue_id::text FROM member_handovers WHERE workspace_id=$1 AND completed_at IS NOT NULL`, ws).Scan(&checklist)
	for key, value := range map[string]string{"DINGTALK_CLIENT_ID": "fixture-id", "DINGTALK_CLIENT_SECRET": "fixture-secret", "DINGTALK_ROBOT_CODE": "fixture-robot"} {
		t.Setenv(key, value)
	}
	canView := func(_ context.Context, w, u, i string) (bool, error) {
		if w != ws || u != recipient || i != checklist {
			t.Errorf("wrong access arguments: %s %s %s", w, u, i)
		}
		return true, nil
	}
	if err := FlushMemberHandoverNotifications(ctx, testHandler.TxStarter, canView); err != nil {
		t.Fatal(err)
	}
	if fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND status='pending' AND last_error='recipient_unbound'`, ws) != 1 {
		t.Fatal("unbound recipient lost notification intent")
	}
	dingID := "handover-test-" + recipient
	fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": recipient, "active": true, "login_only": false, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
	fx.Exec(t, `UPDATE member_handover_notifications SET next_attempt_at=now() WHERE workspace_id=$1`, ws)
	for range 2 {
		if err := FlushMemberHandoverNotifications(ctx, testHandler.TxStarter, canView); err != nil {
			t.Fatal(err)
		}
	}
	if fx.Count(t, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1`, ws) != 1 {
		t.Fatal("Mission routing did not recover idempotently")
	}
	if fx.Count(t, `SELECT count(*) FROM member_handovers WHERE workspace_id=$1 AND completed_at IS NOT NULL`, ws) != 1 {
		t.Fatal("notification recovery repeated business handover")
	}
	for _, phase := range []string{"staged", "delivered", "failed"} {
		if phase != "staged" {
			fx.Exec(t, `UPDATE dingtalk_notify_outbox SET status=$2 WHERE workspace_id=$1`, ws, phase)
		}
		tx, err := testHandler.TxStarter.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		statuses, statusErr := loadMemberHandoverStatuses(ctx, tx, ws, testUserID)
		tx.Rollback(ctx)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if statuses[source] == nil || statuses[source].Status != "completed" || statuses[source].NotificationStatus != phase {
			t.Fatalf("notification %s misreported: %+v", phase, statuses[source])
		}
	}
	statusTx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statuses, statusErr := loadMemberHandoverStatuses(ctx, statusTx, ws, recipient)
	statusTx.Rollback(ctx)
	if statusErr != nil || len(statuses) != 0 {
		t.Fatalf("ordinary member received owner diagnostics: %+v %v", statuses, statusErr)
	}
	tx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stageHandoverNotification(ctx, tx, ws, recipient, "rollback-fixture", "", handoverNotificationPayload{Text: "must roll back"}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if fx.Count(t, `SELECT count(*) FROM member_handover_notifications WHERE workspace_id=$1 AND event_id='rollback-fixture'`, ws) != 0 {
		t.Fatal("intent escaped business rollback")
	}
}
