package main

import (
	"context"
	"testing"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
)

type dingtalkNotifyResolverStub struct {
	bindings map[string]notify.MemberBinding
}

func (s dingtalkNotifyResolverStub) MemberBinding(_ context.Context, workspaceID, memberID string) (notify.MemberBinding, bool, error) {
	binding, ok := s.bindings[memberID]
	if ok {
		binding.WorkspaceID = workspaceID
		binding.MemberID = memberID
	}
	return binding, ok, nil
}

func (dingtalkNotifyResolverStub) AgentChannels(context.Context, string, string) ([]notify.AgentChannel, error) {
	return nil, nil
}

func TestDingTalkNotifyRuntimeEnqueuesEveryStructuredMemberMention(t *testing.T) {
	const (
		workspaceID = "workspace-1"
		memberA     = "11111111-1111-1111-1111-111111111111"
		memberB     = "22222222-2222-2222-2222-222222222222"
	)
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			memberA: {DingUserID: "ding-user-a", Active: true},
			memberB: {DingUserID: "ding-user-b", Active: true},
		}},
	}

	runtime.handleComment(events.Event{
		WorkspaceID: workspaceID,
		ActorType:   "member",
		ActorID:     "author-1",
		Payload: map[string]any{"comment": handler.CommentResponse{
			ID:         "comment-1",
			AuthorType: "member",
			Content: "请 [@zhangchang](mention://member/" + memberA + ") 和 " +
				"[@liqun](mention://member/" + memberB + ") 一起确认",
		}},
	})

	items := store.Snapshot()
	if len(items) != 2 {
		t.Fatalf("outbox item count = %d, want 2", len(items))
	}
	wantRecipients := map[string]string{memberA: "ding-user-a", memberB: "ding-user-b"}
	for _, item := range items {
		wantDingUserID, ok := wantRecipients[item.Message.TargetID]
		if !ok {
			t.Fatalf("unexpected target %q", item.Message.TargetID)
		}
		if item.Message.DingUserID != wantDingUserID {
			t.Errorf("target %s DingTalk user = %q, want %q", item.Message.TargetID, item.Message.DingUserID, wantDingUserID)
		}
		if item.Message.ChannelType != "p2p" || item.Status != notify.StatusPending {
			t.Errorf("target %s channel/status = %s/%s, want p2p/pending", item.Message.TargetID, item.Message.ChannelType, item.Status)
		}
		delete(wantRecipients, item.Message.TargetID)
	}
	if len(wantRecipients) != 0 {
		t.Fatalf("missing targets: %+v", wantRecipients)
	}
}
