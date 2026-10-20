package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestIssueAccessRequestNotificationsReachInboxAndDingTalk(t *testing.T) {
	enableProjectAuthorizationForTest(t)
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
	})
	h := *testHandler
	h.Bus = events.New()
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
			if len(received) != before+1 || received[before].RecipientID != owner {
				t.Fatalf("create must notify the owner once: %#v", received[before:])
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
	ctx := context.Background()
	ws := dbfx.Workspace(t, "Rollback notifications", "rollback-notifications")
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, testUserID, "owner")
	requester := dbfx.User(t, "Rollback requester", "rollback-requester@example.test")
	fx.Member(t, ws, requester, "member")
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
	if count != 0 || requests != 0 || inbox != 0 {
		t.Fatalf("failed commit left events/requests/inbox=%d/%d/%d", count, requests, inbox)
	}
}
