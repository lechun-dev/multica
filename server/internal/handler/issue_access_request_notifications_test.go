package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestIssueAccessRequestNotificationsReachInboxAndDingTalk(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	enableAccessRequestRobotForTest(t)
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Request notifications", "request-notifications")
	owner := dbfx.User(t, "Notification owner", "notification-owner@example.test")
	requester := dbfx.User(t, "Notification requester", "notification-requester@example.test")
	fx := testutil.New(testPool, ws, owner)
	var hasIdentities bool
	if err := testPool.QueryRow(ctx, `SELECT to_regclass('dingtalk_notify_identities') IS NOT NULL`).Scan(&hasIdentities); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{owner, requester} {
		fx.Member(t, ws, user, "member")
		if !hasIdentities {
			continue
		}
		dingID := "request-notification-" + user
		fx.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
			"ding_user_id": dingID, "multica_user_id": user, "active": true, "login_only": false, "updated_at": time.Now(),
		}, "ding_user_id=$1", dingID)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM dingtalk_personal_message WHERE workspace_id=$1`, ws)
		_, _ = testPool.Exec(ctx, `DELETE FROM dingtalk_notify_outbox WHERE workspace_id=$1`, ws)
	})
	h := *testHandler
	h.Bus = events.New()
	h.cfg.AppURL = "https://app.example.test"
	var received []InboxItemResponse
	h.Bus.Subscribe(protocol.EventInboxNew, func(event events.Event) {
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			t.Errorf("unexpected inbox payload: %#v", event.Payload)
			return
		}
		itemMap, ok := payload["item"].(map[string]any)
		if !ok {
			t.Errorf("unexpected inbox item: %#v", payload["item"])
			return
		}
		encoded, err := json.Marshal(itemMap)
		var item InboxItemResponse
		if err != nil || json.Unmarshal(encoded, &item) != nil {
			t.Errorf("invalid inbox item: %#v", itemMap)
			return
		}
		var committed bool
		if err := testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_item WHERE workspace_id=$1 AND id=$2)`, ws, item.ID).Scan(&committed); err != nil || !committed {
			t.Errorf("inbox event must follow commit: committed=%v err=%v", committed, err)
		}
		if event.WorkspaceID != ws || item.WorkspaceID != ws || item.Type != "task_access_request" || item.Read || item.Archived {
			t.Errorf("invalid inbox event: %#v / %#v", event, item)
		}
		received = append(received, item)
	})

	for _, action := range []string{"approve", "reject"} {
		t.Run(action, func(t *testing.T) {
			issue := fx.Issue(t, "Notification task "+action)
			body := map[string]string{"requested_role": "viewer", "idempotency_key": "notification-" + action}
			var request issueAccessRequestResponse
			before := len(received)
			testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(requester, ws, http.MethodPost, issue, "", body)).Want(http.StatusCreated).JSON(&request)
			if hasIdentities {
				var queued int
				if err := testPool.QueryRow(ctx, `SELECT count(*) FROM dingtalk_personal_message WHERE workspace_id=$1 AND comment_id=$2 AND sender_user_id=$3 AND recipient_user_id=$4 AND status='pending'`, ws, request.ID, requester, owner).Scan(&queued); err != nil || queued != 1 {
					t.Fatalf("requested DingTalk queue rows=%d, err=%v; want 1", queued, err)
				}
			}
			if hasIdentities {
				var markdown string
				if err := testPool.QueryRow(ctx, `SELECT markdown FROM dingtalk_personal_message WHERE workspace_id=$1 AND comment_id=$2 AND recipient_user_id=$3`, ws, request.ID, owner).Scan(&markdown); err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"Notification requester", "可查看", "Notification task " + action, "来源：", "[查看并处理申请](https://app.example.test/request-notifications/issues/", "?access_request=" + request.ID} {
					if !strings.Contains(markdown, want) {
						t.Fatalf("DingTalk copy missing %q: %s", want, markdown)
					}
				}
				if strings.Contains(markdown, requester) || strings.Contains(markdown, "viewer") {
					t.Fatalf("internal user or role leaked: %s", markdown)
				}
			}
			if hasIdentities {
				assertAccessRequestRobotNotification(t, ws, request.ID, owner, "pending", "Notification requester", "Notification task "+action)
			}
			if len(received) != before+1 || received[before].RecipientID != owner {
				t.Fatalf("create must notify the owner once: %#v", received[before:])
			}
			if received[before].Body == nil || !strings.Contains(*received[before].Body, "Notification requester 申请「可查看」权限") || !strings.Contains(received[before].Title, "Notification task "+action) {
				t.Fatalf("inbox copy missing applicant, role or task: %+v", received[before])
			}
			testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(requester, ws, http.MethodPost, issue, "", body)).Want(http.StatusOK)
			if len(received) != before+1 {
				t.Fatalf("transport retry published duplicate inbox event")
			}
			review := func() *http.Request {
				return withURLParams(issueAccessHTTPRequest(owner, ws, http.MethodPost, issue, "", map[string]string{"action": action}), "id", issue, "requestID", request.ID)
			}
			failed := h
			failed.TxStarter = rollbackOnCommitTxStarter{pool: testPool}
			testutil.Call(t, failed.ReviewIssueAccessRequest, review()).Want(http.StatusInternalServerError)
			if len(received) != before+1 {
				t.Fatal("failed review commit published an inbox event")
			}
			testutil.Call(t, h.ReviewIssueAccessRequest, review()).Want(http.StatusOK)
			if len(received) != before+2 || received[before+1].RecipientID != requester {
				t.Fatalf("review must notify requester once: %#v", received[before:])
			}
			testutil.Call(t, h.ReviewIssueAccessRequest, review()).Want(http.StatusConflict)
			if len(received) != before+2 {
				t.Fatal("repeated review published duplicate inbox event")
			}
			if hasIdentities {
				outcome := map[string]string{"approve": "approved", "reject": "rejected"}[action]
				assertAccessRequestRobotNotification(t, ws, request.ID, requester, outcome, "申请的", "Notification task "+action)
				var robots int
				if err := testPool.QueryRow(ctx, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1`, ws).Scan(&robots); err != nil || robots != (before/2+1)*2 {
					t.Fatalf("robot retries/failed commits must not add messages: count=%d err=%v", robots, err)
				}
			}
			var queued, failures int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM dingtalk_personal_message WHERE workspace_id=$1 AND comment_id=$2 AND status='pending'`, ws, request.ID).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM activity_log WHERE workspace_id=$1 AND details->>'request_id'=$2 AND action='task_access_request_dingtalk_enqueue_failed'`, ws, request.ID).Scan(&failures); err != nil {
				t.Fatal(err)
			}
			if hasIdentities && (queued != 2 || failures != 0) {
				t.Fatalf("DingTalk requested/result rows=%d, enqueue errors=%d; want 2/0", queued, failures)
			}
			if !hasIdentities {
				t.Log("optional DingTalk identity extension absent; verified inbox delivery only")
			}
		})
	}
}

func TestIssueAccessRequestNotificationsDoNotPublishOnFailedCommit(t *testing.T) {
	enableProjectAuthorizationForTest(t)
	enableAccessRequestRobotForTest(t)
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Rollback notifications", "rollback-notifications")
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	requester := dbfx.User(t, "Rollback requester", "rollback-requester@example.test")
	fx.Member(t, ws, requester, "member")
	seedAccessRequestDingTalkBinding(t, fx, testUserID)
	seedAccessRequestDingTalkBinding(t, fx, requester)
	cleanupAccessRequestDingTalkQueues(t, ws)
	issue := fx.Issue(t, "Rollback notification task")
	h := *testHandler
	h.Bus = events.New()
	h.TxStarter = rollbackOnCommitTxStarter{pool: testPool}
	count := 0
	h.Bus.Subscribe(protocol.EventInboxNew, func(events.Event) { count++ })
	body := map[string]string{"requested_role": "viewer", "idempotency_key": "notification-rollback"}
	testutil.Call(t, h.CreateIssueAccessRequest, issueAccessHTTPRequest(requester, ws, http.MethodPost, issue, "", body)).Want(http.StatusInternalServerError)
	var requests, inbox int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM projectauth_access_requests WHERE workspace_id=$1`, ws).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1`, ws).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"dingtalk_notify_outbox", "dingtalk_personal_message"} {
		var rows int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id=$1`, ws).Scan(&rows); err != nil || rows != 0 {
			t.Errorf("failed commit left %s rows=%d, err=%v", table, rows, err)
		}
	}
	if count != 0 || requests != 0 || inbox != 0 {
		t.Fatalf("failed commit left events/requests/inbox=%d/%d/%d", count, requests, inbox)
	}
}

func TestIssueAccessRequestReapplyAfterApprovalAndRevocation(t *testing.T) {
	enableAccessRequestRobotForTest(t)
	for _, scope := range []string{"projectless", "project"} {
		t.Run(scope, func(t *testing.T) {
			enableProjectAuthorizationForTest(t)
			ctx := context.Background()
			ws := dbfx.Workspace(t, "Reapply notifications", "reapply-notifications-"+scope)
			fx := testutil.New(testPool, ws, testUserID)
			fx.Member(t, ws, testUserID, "owner")
			firstUser := dbfx.User(t, "First applicant", "reapply-first-"+scope+"@example.test")
			secondUser := dbfx.User(t, "Second applicant", "reapply-second-"+scope+"@example.test")
			fx.Member(t, ws, firstUser, "member")
			fx.Member(t, ws, secondUser, "member")
			for _, user := range []string{testUserID, firstUser, secondUser} {
				seedAccessRequestDingTalkBinding(t, fx, user)
			}
			cleanupAccessRequestDingTalkQueues(t, ws)
			var project string
			if scope == "project" {
				project = fx.Project(t, "Reapply project")
			}
			cols := testutil.Cols{}
			if project != "" {
				cols["project_id"] = project
			}
			issue := fx.Issue(t, "Reapply task", cols)
			create := func(user, key string, status int) issueAccessRequestResponse {
				t.Helper()
				var out issueAccessRequestResponse
				testutil.Call(t, testHandler.CreateIssueAccessRequest, issueAccessHTTPRequest(user, ws, http.MethodPost, issue, "", map[string]string{"requested_role": "viewer", "idempotency_key": key})).Want(status).JSON(&out)
				return out
			}
			first := create(firstUser, "first", http.StatusCreated)
			second := create(secondUser, "second", http.StatusCreated)
			var firstInbox string
			fx.QueryRow(t, `SELECT id::text FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2 AND details->>'access_request_id'=$3`, ws, testUserID, first.ID).Scan(&firstInbox)
			var before []InboxItemResponse
			testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox), inboxRequest(http.MethodGet, "/api/inbox", ws)).Want(http.StatusOK).JSON(&before)
			if len(before) != 2 {
				t.Fatalf("two applicants must produce two notices: %+v", before)
			}
			review := withURLParams(issueAccessHTTPRequest(testUserID, ws, http.MethodPost, issue, "", map[string]string{"action": "approve"}), "id", issue, "requestID", first.ID)
			testutil.Call(t, testHandler.ReviewIssueAccessRequest, review).Want(http.StatusOK)
			archive := withURLParams(inboxRequest(http.MethodPost, "/api/inbox/"+firstInbox+"/archive", ws), "id", firstInbox)
			testutil.Call(t, inboxWorkspaceHandler(testHandler.ArchiveInboxItem), archive).Want(http.StatusOK)
			// 2026-10-20 coder(lq): Removing a previously approved grant must not
			// reuse the old request or its dismissed notification on explicit reapply.
			revoke := issueAccessHTTPRequest(testUserID, ws, http.MethodDelete, issue, "", map[string]string{"subject_type": "user", "subject_id": firstUser, "role": "viewer", "scope": "task"})
			testutil.Call(t, testHandler.RevokeIssueAccessGrant, revoke).Want(http.StatusNoContent)
			var grants int
			fx.QueryRow(t, `SELECT (SELECT count(*) FROM projectauth_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3) + (SELECT count(*) FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3)`, ws, issue, firstUser).Scan(&grants)
			if grants != 0 {
				t.Fatalf("grant was not revoked: %d", grants)
			}
			again := create(firstUser, "reapply", http.StatusCreated)
			if again.ID == first.ID || again.Status != accessRequestPending {
				t.Fatalf("reapply did not create a new pending request: %+v", again)
			}
			retry := create(firstUser, "reapply", http.StatusOK)
			pendingRetry := create(firstUser, "different-key-same-pending", http.StatusOK)
			if retry.ID != again.ID || pendingRetry.ID != again.ID {
				t.Fatal("duplicate pending submissions must not create extra requests")
			}
			var active []InboxItemResponse
			testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox), inboxRequest(http.MethodGet, "/api/inbox", ws)).Want(http.StatusOK).JSON(&active)
			seen := map[string]bool{}
			for _, row := range active {
				var details map[string]string
				if err := json.Unmarshal(row.Details, &details); err != nil {
					t.Fatal(err)
				}
				seen[details["access_request_id"]] = true
			}
			if len(active) != 2 || !seen[second.ID] || !seen[again.ID] {
				t.Fatalf("reapply displaced the other applicant or reused dismissed notice: %+v", active)
			}
			var history []InboxItemResponse
			testutil.Call(t, inboxWorkspaceHandler(testHandler.ListArchivedInbox), inboxRequest(http.MethodGet, "/api/inbox/archived", ws)).Want(http.StatusOK).JSON(&history)
			if len(history) != 1 || history[0].ID != firstInbox {
				t.Fatalf("reapply hid archived history: %+v", history)
			}
			for _, request := range []issueAccessRequestResponse{first, second, again} {
				var robots int
				if err := testPool.QueryRow(ctx, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1 AND event_id=$2 AND target_id=$3`, ws, "task-access-request:"+request.ID+":pending", testUserID).Scan(&robots); err != nil || robots != 1 {
					t.Errorf("each application needs its own robot notice, request=%s rows=%d err=%v", request.ID, robots, err)
				}
			}
			var robotTotal int
			fx.QueryRow(t, `SELECT count(*) FROM dingtalk_notify_outbox WHERE workspace_id=$1`, ws).Scan(&robotTotal)
			if robotTotal != 4 {
				t.Errorf("three applications plus one decision must produce 4 robot messages, got %d", robotTotal)
			}
			var notices int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2 AND type='task_access_request'`, ws, testUserID).Scan(&notices); err != nil || notices != 3 {
				t.Fatalf("owner notices=%d want 3 err=%v", notices, err)
			}
		})
	}
}

// 2026-10-20 coder(lq): Fake credentials only enable outbox creation; these tests
// never start the runtime worker or call DingTalk.
func enableAccessRequestRobotForTest(t *testing.T) {
	t.Helper()
	ensureDepartureBindings(t)
	t.Setenv("DINGTALK_CLIENT_ID", "test-robot-app")
	t.Setenv("DINGTALK_CLIENT_SECRET", "test-not-a-real-secret")
	t.Setenv("DINGTALK_ROBOT_CODE", "test-robot-code")
}

func assertAccessRequestRobotNotification(t *testing.T, workspace, request, recipient, event string, copyParts ...string) {
	t.Helper()
	var message notify.Message
	var status string
	err := testPool.QueryRow(context.Background(), `SELECT event_id,workspace_id,target_id,target_kind,
		COALESCE(ding_user_id,''),channel_type,message_text,status FROM dingtalk_notify_outbox
		WHERE workspace_id=$1 AND event_id=$2 AND target_id=$3`, workspace, "task-access-request:"+request+":"+event, recipient).
		Scan(&message.EventID, &message.WorkspaceID, &message.TargetID, &message.TargetKind, &message.DingUserID, &message.ChannelType, &message.Text, &status)
	if err != nil {
		t.Fatalf("robot notification missing: %v", err)
	}
	if message.TargetKind != "member" || message.DingUserID != "request-notification-"+recipient || message.ChannelType != "p2p" || status != notify.StatusPending {
		t.Fatalf("wrong robot recipient or status: %+v / %s", message, status)
	}
	for _, part := range append(copyParts, "可查看", "来源：", "https://app.example.test/") {
		if !strings.Contains(message.Text, part) {
			t.Errorf("robot copy missing %q: %s", part, message.Text)
		}
	}
	if event == "pending" && !strings.Contains(message.Text, "?access_request="+request) {
		t.Errorf("robot request missing review link: %s", message.Text)
	}
	if event != "pending" && strings.Contains(message.Text, "?access_request=") {
		t.Errorf("result must not offer review action: %s", message.Text)
	}
}
