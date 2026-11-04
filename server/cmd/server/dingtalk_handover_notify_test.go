package main

import (
	"context"
	"errors"
	"testing"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type handoverDeliveryRecorder struct {
	messages []notify.Message
}

func (p *handoverDeliveryRecorder) Send(_ context.Context, message notify.Message) error {
	p.messages = append(p.messages, message)
	return nil
}

func TestHandoverDeliveryAllowsOwnerButRejectsUnavailableRecipient(t *testing.T) {
	for _, state := range []string{"owner", "promoted_after_queue", "disabled_owner", "removed_owner"} {
		t.Run(state, func(t *testing.T) {
			fx := testutil.New(testPool, testWorkspaceID, testUserID)
			recipient := fx.User(t, "Receiving leader", "handover-delivery-"+state+"@example.test")
			role := "owner"
			if state == "promoted_after_queue" {
				role = "member"
			}
			fx.Member(t, testWorkspaceID, recipient, role)
			message := notify.Message{EventID: "handover-summary:" + recipient, WorkspaceID: testWorkspaceID,
				TargetID: recipient, TargetKind: "member", DingUserID: "ding-" + recipient, ChannelType: "p2p", Text: "Work list"}
			switch state {
			case "promoted_after_queue":
				fx.Exec(t, `UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, recipient)
			case "disabled_owner":
				fx.Exec(t, `UPDATE "user" SET account_disabled_at=now() WHERE id=$1`, recipient)
			case "removed_owner":
				fx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, recipient)
			}
			next := &handoverDeliveryRecorder{}
			err := (loggingDingTalkNotifyProvider{next: next, pool: testPool}).Send(context.Background(), message)
			if state == "owner" || state == "promoted_after_queue" {
				if err != nil || len(next.messages) != 1 || next.messages[0].TargetID != recipient {
					t.Fatalf("valid owner leader did not receive their notification: %v %+v", err, next.messages)
				}
			} else {
				var suppressed notify.SuppressedDelivery
				if !errors.As(err, &suppressed) || len(next.messages) != 0 {
					t.Fatalf("unavailable leader was not suppressed: %v %+v", err, next.messages)
				}
			}
		})
	}
}
