package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIssueAccessRequestDingTalkChannelsAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		failRobot, failPersonal, failAudit bool
		unboundSender, missingRobotConfig  bool
		wantRobot, wantPersonal, wantAudit int
	}{
		{name: "both", wantRobot: 1, wantPersonal: 1},
		{name: "robot fails", failRobot: true, wantPersonal: 1, wantAudit: 1},
		{name: "personal fails", failPersonal: true, wantRobot: 1, wantAudit: 1},
		{name: "both fail", failRobot: true, failPersonal: true, wantAudit: 2},
		{name: "failure audit fails", failRobot: true, failAudit: true, wantPersonal: 1},
		{name: "sender not bound", unboundSender: true, wantRobot: 1},
		{name: "robot not configured", missingRobotConfig: true, wantPersonal: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableProjectAuthorizationForTest(t)
			enableAccessRequestRobotForTest(t)
			if tc.missingRobotConfig {
				t.Setenv("DINGTALK_ROBOT_CODE", "")
			}
			ctx := context.Background()
			ws := dbfx.Workspace(t, "Independent notification", "independent-robot")
			owner := dbfx.User(t, "Notification owner", "independent-owner@example.test")
			requester := dbfx.User(t, "Notification requester", "independent-requester@example.test")
			fx := testutil.New(testPool, ws, owner)
			for _, id := range []string{owner, requester} {
				fx.Member(t, ws, id, "member")
				if id == requester && tc.unboundSender {
					continue
				}
				seedAccessRequestDingTalkBinding(t, fx, id)
			}
			cleanupAccessRequestDingTalkQueues(t, ws)
			issue := fx.Issue(t, "Independent channels")
			h := *testHandler
			h.cfg.AppURL = "https://app.example.test"
			h.TxStarter = accessRequestFailingDingTalkStarter{pool: testPool, robot: tc.failRobot, personal: tc.failPersonal, audit: tc.failAudit}
			body := map[string]string{"requested_role": "viewer", "idempotency_key": "independent"}
			var request issueAccessRequestResponse
			testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(requester, ws, http.MethodPost, issue, "", body)).Want(http.StatusCreated).JSON(&request)
			testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(requester, ws, http.MethodPost, issue, "", body)).Want(http.StatusOK)
			for _, count := range []struct {
				name, query string
				want        int
			}{
				{"robot", `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1`, tc.wantRobot},
				{"personal", `SELECT count(*) FROM dingtalk_personal_message WHERE workspace_id=$1`, tc.wantPersonal},
				{"audit", `SELECT count(*) FROM activity_log WHERE workspace_id=$1 AND action='task_access_request_dingtalk_enqueue_failed'`, tc.wantAudit},
				{"request", `SELECT count(*) FROM projectauth_access_requests WHERE workspace_id=$1`, 1},
				{"inbox", `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND type='task_access_request'`, 1},
			} {
				var got int
				if err := testPool.QueryRow(ctx, count.query, ws).Scan(&got); err != nil || got != count.want {
					t.Errorf("%s count=%d, want %d; err=%v", count.name, got, count.want, err)
				}
			}
			if tc.wantRobot != 0 {
				assertAccessRequestRobotNotification(t, ws, request.ID, owner, "pending", "Notification requester", "Independent channels")
			}
		})
	}
}

func seedAccessRequestDingTalkBinding(t *testing.T, fx *testutil.Fixture, user string) {
	t.Helper()
	dingID := "request-notification-" + user
	fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
		"ding_user_id": dingID, "multica_user_id": user, "active": true, "login_only": false, "updated_at": time.Now(),
	}, "ding_user_id=$1", dingID)
}

func cleanupAccessRequestDingTalkQueues(t *testing.T, workspace string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM dingtalk_notify_outbox WHERE workspace_id=$1`, workspace)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM dingtalk_personal_message WHERE workspace_id=$1`, workspace)
	})
}

type accessRequestFailingDingTalkStarter struct {
	pool                   *pgxpool.Pool
	robot, personal, audit bool
}

func (s accessRequestFailingDingTalkStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return accessRequestFailingDingTalkTx{Tx: tx, robot: s.robot, personal: s.personal, audit: s.audit}, nil
}

type accessRequestFailingDingTalkTx struct {
	pgx.Tx
	robot, personal, audit bool
}

func (tx accessRequestFailingDingTalkTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if (tx.robot && strings.Contains(query, "INSERT INTO dingtalk_notify_outbox")) ||
		(tx.personal && strings.Contains(query, "INSERT INTO dingtalk_personal_message")) ||
		(tx.audit && strings.Contains(query, "INSERT INTO activity_log") && len(args) > 3 && args[3] == "task_access_request_dingtalk_enqueue_failed") {
		// 2026-10-20 coder(lq): A real SQL failure aborts the transaction until
		// the channel savepoint is rolled back; a synthetic Go error cannot test it.
		return tx.Tx.Exec(ctx, `SELECT 1/0`)
	}
	return tx.Tx.Exec(ctx, query, args...)
}

func TestIssueAccessRequestRobotRecipientEligibility(t *testing.T) {
	for _, state := range []string{"active", "inactive", "login only", "unbound", "not a member", "disabled"} {
		t.Run(state, func(t *testing.T) {
			enableAccessRequestRobotForTest(t)
			ctx := context.Background()
			ws := dbfx.Workspace(t, "Robot recipient", "robot-recipient")
			user := dbfx.User(t, "Robot recipient", "robot-recipient@example.test")
			fx := testutil.New(testPool, ws, user)
			if state != "not a member" {
				fx.Member(t, ws, user, "member")
			}
			if state != "unbound" {
				seedAccessRequestDingTalkBinding(t, fx, user)
			}
			if state == "inactive" || state == "login only" {
				_, err := testPool.Exec(ctx, `UPDATE dingtalk_notify_identities SET active=$2,login_only=$3 WHERE multica_user_id=$1`, user, state != "inactive", state == "login only")
				if err != nil {
					t.Fatal(err)
				}
			}
			if state == "disabled" {
				if _, err := testPool.Exec(ctx, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, user); err != nil {
					t.Fatal(err)
				}
			}
			cleanupAccessRequestDingTalkQueues(t, ws)
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			request := issueAccessRequestResponse{ID: user, WorkspaceID: ws}
			// 2026-10-20 coder(lq): Exercise outbox idempotency directly, independently
			// of the request endpoint's own delivery ledger.
			for i := 0; i < 2; i++ {
				if err := enqueueAccessRequestRobot(ctx, tx, request, user, "pending", "permission notification"); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1`, ws).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if state == "active" {
				want = 1
			}
			if count != want {
				t.Fatalf("robot recipient state %s count=%d, want %d", state, count, want)
			}
		})
	}
}
