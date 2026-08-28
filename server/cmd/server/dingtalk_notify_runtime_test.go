package main

import (
	"context"
	"errors"
	"testing"
	"time"

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

func agentOwnerStub(owners map[string]string) func(context.Context, string, string) (string, error) {
	return func(_ context.Context, _, agentID string) (string, error) {
		return owners[agentID], nil
	}
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

func TestDingTalkNotifyRuntimeNotifiesAgentOwnerOnlyForOtherActors(t *testing.T) {
	const (
		workspaceID = "workspace-agent-owner"
		ownerID     = "11111111-1111-1111-1111-111111111111"
		agentID     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		otherID     = "22222222-2222-2222-2222-222222222222"
	)
	newRuntime := func() (*dingtalkNotifyRuntime, *notify.MemoryStore) {
		store := notify.NewMemoryStore()
		return &dingtalkNotifyRuntime{
			store: store,
			resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
				ownerID: {DingUserID: "ding-owner", Active: true},
			}},
			agentOwner:         agentOwnerStub(map[string]string{agentID: ownerID}),
			agentOwnerMentions: true,
		}, store
	}
	comment := func(actorType, actorID, id string) events.Event {
		return events.Event{WorkspaceID: workspaceID, ActorType: actorType, ActorID: actorID,
			Payload: map[string]any{"comment": handler.CommentResponse{ID: id, AuthorType: actorType, AuthorID: actorID,
				Content: "请 [@我的Agent](mention://agent/" + agentID + ") 处理"}}}
	}

	otherRuntime, otherStore := newRuntime()
	otherRuntime.handleComment(comment("member", otherID, "comment-other"))
	if items := otherStore.Snapshot(); len(items) != 1 || items[0].Message.TargetID != ownerID || items[0].Message.DingUserID != "ding-owner" {
		t.Fatalf("other member should notify owner, items=%+v", items)
	}

	selfRuntime, selfStore := newRuntime()
	selfRuntime.handleComment(comment("member", ownerID, "comment-self"))
	if items := selfStore.Snapshot(); len(items) != 0 {
		t.Fatalf("owner's own Agent mention should not notify, items=%+v", items)
	}

	agentRuntime, agentStore := newRuntime()
	agentRuntime.handleComment(comment("agent", agentID, "comment-agent"))
	if items := agentStore.Snapshot(); len(items) != 0 {
		t.Fatalf("Agent-authored mention should not notify its owner, items=%+v", items)
	}
}

func TestDingTalkNotifyRuntimeDeduplicatesDirectAndAgentOwnerMention(t *testing.T) {
	const ownerID = "11111111-1111-1111-1111-111111111111"
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			ownerID: {DingUserID: "ding-owner", Active: true},
		}},
		agentOwner:         agentOwnerStub(map[string]string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa": ownerID}),
		agentOwnerMentions: true,
	}
	runtime.handleComment(events.Event{WorkspaceID: "workspace-dedupe", ActorType: "member", ActorID: "other",
		Payload: map[string]any{"comment": handler.CommentResponse{ID: "comment-dedupe", AuthorType: "member", AuthorID: "other",
			Content: "[@成员](mention://member/" + ownerID + ") [@Agent](mention://agent/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa)"}}})
	if items := store.Snapshot(); len(items) != 1 {
		t.Fatalf("direct and Agent mentions for one owner should dedupe, items=%+v", items)
	}
}

func TestSuperviseDingTalkNotifyWorkerRestartsAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseDingTalkNotifyWorker(ctx, time.Millisecond, time.Millisecond,
			func(context.Context) error {
				calls++
				if calls == 1 {
					return errors.New("temporary database failure")
				}
				cancel()
				return context.Canceled
			})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker supervisor did not stop after cancellation")
	}
	if calls != 2 {
		t.Fatalf("worker run calls = %d, want 2", calls)
	}
}
