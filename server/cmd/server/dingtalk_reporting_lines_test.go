package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This integration test requires a dedicated, migrated fixture database.
func TestDingTalkReportingLineRetention(t *testing.T) {
	ensureDingTalkProfileSyncSchema(t)
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	ws := fx.Workspace(t, "Reporting line fixture", fmt.Sprint("reporting-", time.Now().UnixNano()))
	fx = testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	uid := fx.User(t, "Employee", ws+"@example.test")
	fx.Member(t, ws, uid, "member")
	dingID := "reporting-" + uid
	fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": uid, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
	fx.Cleanup(t, `DELETE FROM projectauth_organizations WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM projectauth_organization_members WHERE workspace_id=$1`, ws)
	fx.Cleanup(t, `DELETE FROM directory_reporting_lines WHERE workspace_id=$1`, ws)
	login := &dingtalkLoginHandler{pool: testPool, host: &handler.Handler{Queries: db.New(testPool), TxStarter: testPool, Bus: events.New()}}
	member := notify.DingTalkDirectoryMember{DingUserID: dingID, Name: "Employee", DepartmentIDs: []string{"42"}, ManagerUserID: "0201243712852502", ManagerReadStatus: "complete"}
	snapshot := notify.DingTalkDirectorySnapshot{Departments: []notify.DingTalkDirectoryDepartment{{ID: "42", Name: "Department"}}, Members: []notify.DingTalkDirectoryMember{member}}
	sync := func() dingtalkOrganizationSyncResult {
		t.Helper()
		result := dingtalkOrganizationSyncResult{}
		if err := login.persistDingTalkDirectory(ctx, ws, testUserID, snapshot, nil, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := sync(); result.ManagersSynced != 1 || result.ManagersFailed != 0 {
		t.Fatalf("unexpected initial counters %+v", result)
	}
	var manager, status string
	var successful, attempted time.Time
	read := func() {
		t.Helper()
		fx.QueryRow(t, `SELECT manager_external_id,sync_status,successful_at,attempted_at FROM directory_reporting_lines WHERE workspace_id=$1 AND user_id=$2`, ws, uid).Scan(&manager, &status, &successful, &attempted)
	}
	read()
	firstSuccess, firstAttempt := successful, attempted
	if manager != "0201243712852502" || status != "complete" {
		t.Fatalf("manager ID lost leading zero: %q / %s", manager, status)
	}
	snapshot.Members[0].ManagerReadStatus = "failed"
	snapshot.Members[0].ManagerUserID = ""
	if result := sync(); result.ManagersFailed != 1 || result.ManagersSynced != 0 {
		t.Fatalf("unexpected failure counters %+v", result)
	}
	read()
	if manager != "0201243712852502" || status != "failed" || !successful.Equal(firstSuccess) || attempted.Before(firstAttempt) {
		t.Fatal("failed read overwrote last successful manager")
	}
	snapshot.Members = nil
	sync()
	read()
	if manager != "0201243712852502" || !successful.Equal(firstSuccess) {
		t.Fatal("missing employee lost retained reporting line")
	}
	member.ManagerUserID = ""
	snapshot.Members = []notify.DingTalkDirectoryMember{member}
	sync()
	read()
	if manager != "" || status != "complete" {
		t.Fatal("successful unset did not clear old manager")
	}
}

type reportingLineFailureStarter struct {
	inner interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	userID     string
	failMarker bool
}

func (s reportingLineFailureStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return reportingLineFailureTx{Tx: tx, userID: s.userID, failMarker: s.failMarker}, nil
}

type reportingLineFailureTx struct {
	pgx.Tx
	userID     string
	failMarker bool
}

func (tx reportingLineFailureTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return reportingLineFailureTx{Tx: nested, userID: tx.userID, failMarker: tx.failMarker}, nil
}

func (tx reportingLineFailureTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if len(args) > 1 && args[1] == tx.userID &&
		(strings.Contains(sql, "INSERT INTO directory_reporting_lines") ||
			(tx.failMarker && strings.Contains(sql, "UPDATE directory_reporting_lines SET sync_status='failed'"))) {
		// A real SQL error aborts the current savepoint. Returning a synthetic
		// error would not verify that the outer directory transaction recovers.
		return tx.Tx.Exec(ctx, `SELECT 1/0`)
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestDingTalkReportingLineWriteFailureDoesNotRollbackDirectory(t *testing.T) {
	for _, failMarker := range []bool{false, true} {
		t.Run(fmt.Sprintf("marker_failure_%t", failMarker), func(t *testing.T) {
			ensureDingTalkProfileSyncSchema(t)
			ctx := context.Background()
			fx := testutil.New(testPool, testWorkspaceID, testUserID)
			ws := fx.Workspace(t, "Reporting isolation", fmt.Sprint("reporting-isolation-", time.Now().UnixNano()))
			fx = testutil.New(testPool, ws, testUserID)
			fx.Member(t, ws, testUserID, "owner")
			fx.Cleanup(t, `DELETE FROM projectauth_organizations WHERE workspace_id=$1`, ws)
			fx.Cleanup(t, `DELETE FROM projectauth_organization_members WHERE workspace_id=$1`, ws)
			fx.Cleanup(t, `DELETE FROM directory_reporting_lines WHERE workspace_id=$1`, ws)
			fx.Cleanup(t, `DELETE FROM member_handover_automation WHERE workspace_id=$1`, ws)
			var members []notify.DingTalkDirectoryMember
			for _, name := range []string{"First", "Later"} {
				uid := fx.User(t, name, ws+name+"@example.test")
				fx.Member(t, ws, uid, "member")
				dingID := "reporting-" + uid
				fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{"ding_user_id": dingID, "multica_user_id": uid, "updated_at": time.Now()}, "ding_user_id=$1", dingID)
				members = append(members, notify.DingTalkDirectoryMember{DingUserID: dingID, Name: name, DepartmentIDs: []string{"42"}, ManagerUserID: "new-manager", ManagerReadStatus: "complete"})
			}
			var first, later string
			fx.QueryRow(t, `SELECT multica_user_id FROM dingtalk_notify_identities WHERE ding_user_id=$1`, members[0].DingUserID).Scan(&first)
			fx.QueryRow(t, `SELECT multica_user_id FROM dingtalk_notify_identities WHERE ding_user_id=$1`, members[1].DingUserID).Scan(&later)
			lastSuccess := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
			fx.InsertNoID(t, "directory_reporting_lines", testutil.Cols{"workspace_id": ws, "user_id": first, "provider": "dingtalk", "external_user_id": members[0].DingUserID, "manager_external_id": "old-manager", "sync_status": "complete", "successful_at": lastSuccess}, "workspace_id=$1 AND user_id=$2", ws, first)
			login := &dingtalkLoginHandler{pool: testPool, host: &handler.Handler{Queries: db.New(testPool), TxStarter: reportingLineFailureStarter{inner: testPool, userID: first, failMarker: failMarker}}}
			snapshot := notify.DingTalkDirectorySnapshot{Departments: []notify.DingTalkDirectoryDepartment{{ID: "42", Name: "Department"}}, Members: members}
			result := dingtalkOrganizationSyncResult{}
			if err := login.persistDingTalkDirectory(ctx, ws, testUserID, snapshot, nil, &result); err != nil {
				t.Fatal(err)
			}
			if result.ManagersFailed != 1 || result.ManagersSynced != 1 || result.MembersCreated != 2 {
				t.Fatalf("reporting failure affected directory/other employee: %+v", result)
			}
			var manager, status string
			var successful time.Time
			fx.QueryRow(t, `SELECT manager_external_id,sync_status,successful_at FROM directory_reporting_lines WHERE workspace_id=$1 AND user_id=$2`, ws, first).Scan(&manager, &status, &successful)
			if manager != "old-manager" || !successful.Equal(lastSuccess) || (!failMarker && status != "failed") {
				t.Fatalf("failed write lost previous relationship: %s %s %v", manager, status, successful)
			}
			if fx.Count(t, `SELECT count(*) FROM member_handover_automation WHERE workspace_id=$1 AND source_user_id=$2 AND manager_sync_failed AND NOT eligible`, ws, first) != 1 {
				t.Fatal("failed reporting-line diagnostics left the old manager trusted")
			}
			if fx.Count(t, `SELECT count(*) FROM directory_reporting_lines WHERE workspace_id=$1 AND user_id=$2 AND manager_external_id='new-manager' AND sync_status='complete'`, ws, later) != 1 ||
				fx.Count(t, `SELECT count(*) FROM projectauth_organization_members WHERE workspace_id=$1`, ws) != 2 {
				t.Fatal("later employee or ordinary memberships did not commit")
			}
		})
	}
}
