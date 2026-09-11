package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func personalMessageRequest(method, path string, body any) *http.Request {
	return testutil.WithHeaders(
		testutil.JSONRequest(method, path, body),
		"X-User-ID", testUserID,
		"X-Workspace-ID", testWorkspaceID,
		"X-Client-Capabilities", protocol.DaemonCapabilityDingTalkPersonalMessageV1,
	)
}

func claimPersonalMessage(t *testing.T, daemonID string) *dingtalkPersonalMessageClaim {
	return claimPersonalMessageWithRetry(t, daemonID, false)
}

func claimPersonalMessageWithRetry(t *testing.T, daemonID string, retryWaiting bool) *dingtalkPersonalMessageClaim {
	t.Helper()
	var response dingtalkPersonalMessageClaimResponse
	testutil.Call(t, testHandler.ClaimDingTalkPersonalMessage, personalMessageRequest(
		http.MethodPost,
		"/api/daemon/dingtalk-personal-messages/claim",
		map[string]any{"daemon_id": daemonID, "retry_waiting": retryWaiting},
	)).Want(http.StatusOK).JSON(&response)
	return response.Message
}

func reportPersonalMessage(t *testing.T, messageID, daemonID, status string) *testutil.Response {
	t.Helper()
	req := personalMessageRequest(http.MethodPost, "/api/daemon/dingtalk-personal-messages/"+messageID+"/result", map[string]string{
		"daemon_id": daemonID,
		"status":    status,
	})
	return testutil.Call(t, testHandler.ReportDingTalkPersonalMessageResult, testutil.WithURLParams(req, "id", messageID))
}

func TestDingTalkPersonalMessageClaimRejectsMalformedJSON(t *testing.T) {
	req := personalMessageRequest(http.MethodPost, "/api/daemon/dingtalk-personal-messages/claim", "{")
	testutil.Call(t, testHandler.ClaimDingTalkPersonalMessage, req).Want(http.StatusBadRequest)
}

func TestDingTalkPersonalMessageResultRequiresDeliveryIDs(t *testing.T) {
	req := personalMessageRequest(http.MethodPost, "/api/daemon/dingtalk-personal-messages/message/result", map[string]string{
		"daemon_id": "daemon-a",
		"status":    "delivered",
	})
	req = testutil.WithURLParams(req, "id", uuid.NewString())
	testutil.Call(t, testHandler.ReportDingTalkPersonalMessageResult, req).Want(http.StatusBadRequest)
}

func TestDingTalkPersonalMessageClaimLeaseAndWaitingState(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	messageID := dbfx.Insert(t, "dingtalk_personal_message", testutil.Cols{
		"workspace_id":           testWorkspaceID,
		"comment_id":             testutil.Raw("gen_random_uuid()"),
		"sender_user_id":         testUserID,
		"sender_ding_user_id":    "sender-ding",
		"recipient_user_id":      testUserID,
		"recipient_ding_user_id": "recipient-ding",
		"markdown":               "## test",
		"idempotency_key":        "handler-personal-lease-" + time.Now().Format("20060102150405.000000000"),
	})

	claimed := claimPersonalMessage(t, "daemon-a")
	if claimed == nil || claimed.ID != messageID {
		t.Fatalf("first claim message = %+v, want id %s", claimed, messageID)
	}
	if claimed.Markdown != "## test" || claimed.RecipientDingUserID != "recipient-ding" {
		t.Fatalf("claimed message lost delivery data: %+v", claimed)
	}
	if second := claimPersonalMessage(t, "daemon-b"); second != nil {
		t.Fatalf("active lease was claimed twice: %+v", second)
	}

	reportPersonalMessage(t, messageID, "daemon-b", "waiting_for_dws_login").Want(http.StatusConflict)
	reportPersonalMessage(t, messageID, "daemon-a", "waiting_for_dws_login").Want(http.StatusOK)

	var state string
	var availableAt time.Time
	var leaseOwner *string
	dbfx.QueryRow(t, `
		SELECT status, available_at, lease_owner
		FROM dingtalk_personal_message WHERE id = $1`, messageID).Scan(&state, &availableAt, &leaseOwner)
	if state != "waiting_for_dws_login" || leaseOwner != nil || !availableAt.After(time.Now().Add(30*time.Second)) {
		t.Fatalf("unexpected waiting state: status=%s available_at=%s lease_owner=%v", state, availableAt, leaseOwner)
	}
}

func TestDingTalkPersonalMessageClaimRetryWaitingBypassesBackoff(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	messageID := dbfx.Insert(t, "dingtalk_personal_message", testutil.Cols{
		"workspace_id":           testWorkspaceID,
		"comment_id":             testutil.Raw("gen_random_uuid()"),
		"sender_user_id":         testUserID,
		"sender_ding_user_id":    "sender-ding",
		"recipient_user_id":      testUserID,
		"recipient_ding_user_id": "recipient-ding",
		"markdown":               "## retry after login",
		"idempotency_key":        "handler-personal-auth-retry-" + time.Now().Format("20060102150405.000000000"),
		"status":                 "waiting_for_dws_login",
		"available_at":           testutil.Raw("now() + interval '1 minute'"),
	})

	if claimed := claimPersonalMessage(t, "daemon-before-login"); claimed != nil {
		t.Fatalf("waiting message was claimable before its backoff elapsed: %+v", claimed)
	}
	claimed := claimPersonalMessageWithRetry(t, "daemon-after-login", true)
	if claimed == nil || claimed.ID != messageID {
		t.Fatalf("forced post-login claim = %+v, want id %s", claimed, messageID)
	}
}

func TestDingTalkPersonalMessageClaimExpiresOldRows(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	messageID := dbfx.Insert(t, "dingtalk_personal_message", testutil.Cols{
		"workspace_id":           testWorkspaceID,
		"comment_id":             testutil.Raw("gen_random_uuid()"),
		"sender_user_id":         testUserID,
		"sender_ding_user_id":    "sender-ding",
		"recipient_user_id":      testUserID,
		"recipient_ding_user_id": "recipient-ding",
		"markdown":               "expired",
		"idempotency_key":        "handler-personal-expired-" + time.Now().Format("20060102150405.000000000"),
		"expires_at":             testutil.Raw("now() - interval '1 second'"),
	})

	if claimed := claimPersonalMessage(t, "daemon-expiry"); claimed != nil {
		t.Fatalf("expired message was claimable: %+v", claimed)
	}
	var state, errorCode string
	dbfx.QueryRow(t, `
		SELECT status, COALESCE(last_error_code, '')
		FROM dingtalk_personal_message WHERE id = $1`, messageID).Scan(&state, &errorCode)
	if state != "expired" || errorCode != "expired" {
		t.Fatalf("expired row state/code = %q/%q", state, errorCode)
	}
}
