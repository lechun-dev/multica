package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/pkg/protocol"
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

func TestPersonalMentionSenderUsesMemberOrOwnedAgentIdentity(t *testing.T) {
	const (
		workspaceID = "workspace-personal-sender"
		ownerID     = "11111111-1111-1111-1111-111111111111"
		agentID     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	runtime := &dingtalkNotifyRuntime{
		agentOwner: agentOwnerStub(map[string]string{agentID: ownerID}),
	}
	if got := runtime.personalMentionSenderUserID(workspaceID, "member", ownerID); got != ownerID {
		t.Fatalf("member sender = %q, want %q", got, ownerID)
	}
	if got := runtime.personalMentionSenderUserID(workspaceID, "agent", agentID); got != ownerID {
		t.Fatalf("owned Agent sender = %q, want owner %q", got, ownerID)
	}
	if got := runtime.personalMentionSenderUserID(workspaceID, "agent", "unowned-agent"); got != "" {
		t.Fatalf("unowned Agent sender = %q, want empty", got)
	}
	if got := runtime.personalMentionSenderUserID(workspaceID, "system", agentID); got != "" {
		t.Fatalf("system sender = %q, want empty", got)
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

func TestDingTalkNotifyRuntimeNotifiesCompletedAgentOwnerAndInitiator(t *testing.T) {
	const (
		workspaceID = "workspace-completion"
		taskID      = "task-completion"
		agentID     = "agent-completion"
		ownerID     = "11111111-1111-1111-1111-111111111111"
		initiatorID = "22222222-2222-2222-2222-222222222222"
	)
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			ownerID:     {DingUserID: "ding-owner", Active: true},
			initiatorID: {DingUserID: "ding-initiator", Active: true},
		}},
		agentOwner: agentOwnerStub(map[string]string{agentID: ownerID}),
	}
	runtime.handleTaskCompleted(events.Event{Type: "task:completed", TaskID: taskID, WorkspaceID: workspaceID, Payload: map[string]any{
		"task_id": taskID, "agent_id": agentID, "initiator_user_id": initiatorID, "output": "已完成测试并生成结果。",
	}})
	items := store.Snapshot()
	if len(items) != 2 {
		t.Fatalf("completion recipients = %d, want 2: %+v", len(items), items)
	}
	want := map[string]string{ownerID: "ding-owner", initiatorID: "ding-initiator"}
	for _, item := range items {
		if want[item.Message.TargetID] != item.Message.DingUserID || item.Message.ChannelType != "p2p" {
			t.Fatalf("unexpected completion message: %+v", item.Message)
		}
		if item.Message.Text != "✅ **MissionOS Agent 已完成执行**\n\n已完成测试并生成结果。\n\n---" {
			t.Fatalf("unexpected completion text: %q", item.Message.Text)
		}
		delete(want, item.Message.TargetID)
	}
	if len(want) != 0 {
		t.Fatalf("missing completion recipients: %+v", want)
	}
}

func TestDingTalkNotifyRuntimeDeduplicatesCompletedOwnerAndInitiator(t *testing.T) {
	const ownerID = "11111111-1111-1111-1111-111111111111"
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			ownerID: {DingUserID: "ding-owner", Active: true},
		}},
		agentOwner: agentOwnerStub(map[string]string{"agent-1": ownerID}),
	}
	runtime.handleTaskCompleted(events.Event{TaskID: "task-1", WorkspaceID: "workspace-1", Payload: map[string]any{
		"task_id": "task-1", "agent_id": "agent-1", "initiator_user_id": ownerID,
	}})
	if items := store.Snapshot(); len(items) != 1 {
		t.Fatalf("same owner/initiator should receive one completion message, items=%+v", items)
	}
}

func TestDingTalkNotifyRuntimeNotifiesMemberAssignedOnIssueCreate(t *testing.T) {
	const memberID = "11111111-1111-1111-1111-111111111111"
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			memberID: {DingUserID: "ding-assignee", Active: true},
		}},
	}
	assigneeType := "member"
	assigneeID := memberID
	runtime.handleIssueCreated(events.Event{
		Type: protocol.EventIssueCreated, WorkspaceID: "workspace-1", ActorType: "member", ActorID: memberID,
		Payload: map[string]any{"issue": handler.IssueResponse{
			ID: "issue-1", WorkspaceID: "workspace-1", Identifier: "MUL-123", Title: "优化手机端登录",
			AssigneeType: &assigneeType, AssigneeID: &assigneeID, Revision: 1,
		}},
	})
	items := store.Snapshot()
	if len(items) != 1 {
		t.Fatalf("created assignment messages=%d, want 1: %+v", len(items), items)
	}
	if item := items[0]; item.Message.TargetID != memberID || item.Message.ChannelType != "p2p" || !strings.Contains(item.Message.Text, "你已成为该任务的负责人") {
		t.Fatalf("unexpected created assignment message: %+v", item.Message)
	}
}

func TestDingTalkNotifyRuntimeSkipsIssueCreateWithoutMemberAssignee(t *testing.T) {
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{store: store, resolver: dingtalkNotifyResolverStub{}}
	runtime.handleIssueCreated(events.Event{
		Type: protocol.EventIssueCreated, WorkspaceID: "workspace-1",
		Payload: map[string]any{"issue": handler.IssueResponse{
			ID: "issue-1", WorkspaceID: "workspace-1", Identifier: "MUL-123", Title: "未分配任务", Revision: 1,
		}},
	})
	if items := store.Snapshot(); len(items) != 0 {
		t.Fatalf("unassigned issue should not enqueue assignment message: %+v", items)
	}
}

func TestDingTalkNotifyRuntimeIgnoresNonMemberIssueCreateAssignees(t *testing.T) {
	for _, assigneeType := range []string{"agent", "squad"} {
		t.Run(assigneeType, func(t *testing.T) {
			store := notify.NewMemoryStore()
			runtime := &dingtalkNotifyRuntime{store: store, resolver: dingtalkNotifyResolverStub{}}
			assigneeID := "assignee-1"
			runtime.handleIssueCreated(events.Event{WorkspaceID: "workspace-1", Payload: map[string]any{"issue": handler.IssueResponse{
				ID: "issue-1", WorkspaceID: "workspace-1", AssigneeType: &assigneeType, AssigneeID: &assigneeID, Revision: 1,
			}}})
			if items := store.Snapshot(); len(items) != 0 {
				t.Fatalf("%s assignee should not receive personal message: %+v", assigneeType, items)
			}
		})
	}
}

func TestDingTalkNotifyRuntimeNotifiesBothSidesOfMemberTransfer(t *testing.T) {
	const (
		oldID   = "11111111-1111-1111-1111-111111111111"
		newID   = "22222222-2222-2222-2222-222222222222"
		actorID = "33333333-3333-3333-3333-333333333333"
	)
	store := notify.NewMemoryStore()
	wakeup := &personalMentionWakeupRecorder{}
	personalKeys := map[string]struct{}{}
	type personalDelivery struct {
		senderID    string
		recipientID string
		text        string
	}
	var personal []personalDelivery
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			oldID: {DingUserID: "ding-old", Active: true},
			newID: {DingUserID: "ding-new", Active: true},
		}},
		canViewIssue: func(context.Context, string, string, string) (bool, error) {
			return true, nil
		},
		displayName: func(_ context.Context, _, kind, id string) (string, error) {
			if kind == "member" && id == actorID {
				return "张畅", nil
			}
			if kind == "member" && id == newID {
				return "李群", nil
			}
			return "", nil
		},
		personalAssignment: func(_ context.Context, issue dingtalkAssignmentIssue, senderID, recipientID string, event notify.AssignmentNotification) (bool, error) {
			key := issue.id + ":" + strconv.FormatInt(issue.revision, 10) + ":" + recipientID
			if _, exists := personalKeys[key]; exists {
				return false, nil
			}
			personalKeys[key] = struct{}{}
			personal = append(personal, personalDelivery{senderID: senderID, recipientID: recipientID, text: notify.FormatPersonalAssignmentText(event)})
			return true, nil
		},
		personalWakeup: wakeup,
	}
	memberType := "member"
	newAssigneeID := newID
	oldAssigneeID := oldID
	updatedAt := "2026-10-13T02:30:00Z"
	event := events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: "workspace-1", ActorType: "member", ActorID: actorID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID: "issue-1", WorkspaceID: "workspace-1", Identifier: "MUL-123", Title: "优化手机端登录",
				AssigneeType: &memberType, AssigneeID: &newAssigneeID, Revision: 7, UpdatedAt: updatedAt,
			},
			"assignee_changed": true, "prev_assignee_type": &memberType, "prev_assignee_id": &oldAssigneeID,
		},
	}
	runtime.handleIssueUpdated(event)
	runtime.handleIssueUpdated(event)

	items := store.Snapshot()
	if len(items) != 2 {
		t.Fatalf("transfer messages=%d, want 2 after replay dedupe: %+v", len(items), items)
	}
	texts := map[string]string{}
	for _, item := range items {
		if item.Message.ChannelType != "p2p" {
			t.Fatalf("assignment message channel=%q, want p2p: %+v", item.Message.ChannelType, item.Message)
		}
		texts[item.Message.TargetID] = item.Message.Text
	}
	wantNew := "📌 **你已成为该任务的负责人**\n\n操作人：张畅\n\n操作时间：2026-10-13 10:30\n\n---\n\n来源：MUL-123 · 优化手机端登录"
	if texts[newID] != wantNew {
		t.Fatalf("new assignee Mission message=%q, want %q", texts[newID], wantNew)
	}
	wantOld := "↗️ **该任务已转交给 李群**\n\n操作人：张畅\n\n操作时间：2026-10-13 10:30\n\n---\n\n来源：MUL-123 · 优化手机端登录"
	if texts[oldID] != wantOld {
		t.Fatalf("old assignee Mission message=%q, want %q", texts[oldID], wantOld)
	}
	if len(personal) != 1 {
		t.Fatalf("personal assignment deliveries=%d, want 1: %+v", len(personal), personal)
	}
	wantPersonal := "你已成为该任务的负责人\n\n操作人：张畅\n\n操作时间：2026-10-13 10:30\n\n---\n\n来源：MUL-123 · 优化手机端登录"
	if got := personal[0]; got.senderID != actorID || got.recipientID != newID || got.text != wantPersonal {
		t.Fatalf("personal assignment delivery=%+v, want sender=%s recipient=%s text=%q", got, actorID, newID, wantPersonal)
	}
	if len(wakeup.users) != 1 || wakeup.users[0] != actorID {
		t.Fatalf("personal assignment wakeups=%v, want [%s]", wakeup.users, actorID)
	}
}

func TestDingTalkNotifyRuntimeAssignsPreviouslyUnassignedIssueWithMissionOnly(t *testing.T) {
	const (
		newID   = "22222222-2222-2222-2222-222222222222"
		actorID = "33333333-3333-3333-3333-333333333333"
	)
	store := notify.NewMemoryStore()
	personalCalls := 0
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			newID: {DingUserID: "ding-new", Active: true},
		}},
		personalAssignment: func(context.Context, dingtalkAssignmentIssue, string, string, notify.AssignmentNotification) (bool, error) {
			personalCalls++
			return true, nil
		},
	}
	memberType := "member"
	runtime.handleIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: "workspace-1", ActorType: "member", ActorID: actorID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID: "issue-3", WorkspaceID: "workspace-1", Identifier: "MUL-125", Title: "补充负责人",
				AssigneeType: &memberType, AssigneeID: ptrString(newID), Revision: 2, UpdatedAt: "2026-10-13T04:00:00Z",
			},
			"assignee_changed": true,
		},
	})

	items := store.Snapshot()
	if len(items) != 1 || items[0].Message.TargetID != newID {
		t.Fatalf("previously unassigned Mission messages=%d, want one for %s: %+v", len(items), newID, items)
	}
	if personalCalls != 0 {
		t.Fatalf("previously unassigned personal deliveries=%d, want 0", personalCalls)
	}
}

func TestDingTalkNotifyRuntimeAgentTransferUsesMissionOnly(t *testing.T) {
	const (
		oldID   = "11111111-1111-1111-1111-111111111111"
		newID   = "22222222-2222-2222-2222-222222222222"
		agentID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	store := notify.NewMemoryStore()
	personalCalls := 0
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			oldID: {DingUserID: "ding-old", Active: true},
			newID: {DingUserID: "ding-new", Active: true},
		}},
		displayName: func(_ context.Context, _, kind, id string) (string, error) {
			switch {
			case kind == "agent" && id == agentID:
				return "任务助手", nil
			case kind == "member" && id == newID:
				return "李群", nil
			default:
				return "", nil
			}
		},
		personalAssignment: func(context.Context, dingtalkAssignmentIssue, string, string, notify.AssignmentNotification) (bool, error) {
			personalCalls++
			return true, nil
		},
	}
	memberType := "member"
	runtime.handleIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: "workspace-1", ActorType: "agent", ActorID: agentID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID: "issue-2", WorkspaceID: "workspace-1", Identifier: "MUL-124", Title: "智能体转交任务",
				AssigneeType: &memberType, AssigneeID: ptrString(newID), Revision: 9, UpdatedAt: "2026-10-13T03:15:00Z",
			},
			"assignee_changed": true, "prev_assignee_type": &memberType, "prev_assignee_id": ptrString(oldID),
		},
	})
	items := store.Snapshot()
	if len(items) != 2 {
		t.Fatalf("agent transfer Mission messages=%d, want 2: %+v", len(items), items)
	}
	for _, item := range items {
		if !strings.Contains(item.Message.Text, "操作人：任务助手") || !strings.Contains(item.Message.Text, "操作时间：2026-10-13 11:15") {
			t.Fatalf("agent transfer message missing operator metadata: %q", item.Message.Text)
		}
	}
	if personalCalls != 0 {
		t.Fatalf("agent transfer personal deliveries=%d, want 0", personalCalls)
	}
}

func ptrString(value string) *string { return &value }

func TestDingTalkNotifyRuntimeHandlesResolvedMapIssuePayload(t *testing.T) {
	const memberID = "11111111-1111-1111-1111-111111111111"
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			memberID: {DingUserID: "ding-assignee", Active: true},
		}},
	}
	runtime.handleIssueUpdated(events.Event{
		Type: protocol.EventIssueUpdated, WorkspaceID: "workspace-1", ActorType: "member", ActorID: memberID,
		Payload: map[string]any{
			"issue": map[string]any{
				"id": "issue-1", "workspace_id": "workspace-1", "identifier": "MUL-124",
				"title": "Autopilot 创建的任务", "assignee_type": "member", "assignee_id": memberID,
				"revision": float64(3),
			},
			"assignee_changed": true,
		},
	})
	items := store.Snapshot()
	if len(items) != 1 {
		t.Fatalf("resolved map assignment messages=%d, want 1: %+v", len(items), items)
	}
	message := items[0].Message
	if message.EventID != "issue-assignment:issue-1:3:assignment_added" || message.TargetID != memberID || !strings.Contains(message.Text, "MUL-124 · Autopilot 创建的任务") {
		t.Fatalf("unexpected resolved map assignment message: %+v", message)
	}
}

func TestDingTalkNotifyRuntimeNotifiesRemovedMemberWithoutReplacement(t *testing.T) {
	const oldID = "11111111-1111-1111-1111-111111111111"
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			oldID: {DingUserID: "ding-old", Active: true},
		}},
	}
	memberType := "member"
	oldAssigneeID := oldID
	runtime.handleIssueUpdated(events.Event{WorkspaceID: "workspace-1", Payload: map[string]any{
		"issue":            handler.IssueResponse{ID: "issue-1", WorkspaceID: "workspace-1", Identifier: "MUL-123", Title: "任务", Revision: 8},
		"assignee_changed": true, "prev_assignee_type": &memberType, "prev_assignee_id": &oldAssigneeID,
	}})
	items := store.Snapshot()
	if len(items) != 1 || items[0].Message.TargetID != oldID || !strings.Contains(items[0].Message.Text, "该任务已转交") {
		t.Fatalf("removed assignment message mismatch: %+v", items)
	}
}

func TestDingTalkNotifyRuntimeSkipsUnchangedOrFormerWorkspaceMember(t *testing.T) {
	const memberID = "11111111-1111-1111-1111-111111111111"
	memberType := "member"
	assigneeID := memberID
	issue := handler.IssueResponse{ID: "issue-1", WorkspaceID: "workspace-1", AssigneeType: &memberType, AssigneeID: &assigneeID, Revision: 9}

	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{
		store: store,
		resolver: dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{
			memberID: {DingUserID: "ding-member", Active: true},
		}},
		memberInWorkspace: func(context.Context, string, string) (bool, error) { return false, nil },
	}
	runtime.handleIssueUpdated(events.Event{WorkspaceID: "workspace-1", Payload: map[string]any{
		"issue": issue, "assignee_changed": true, "prev_assignee_type": &memberType, "prev_assignee_id": &assigneeID,
	}})
	runtime.handleIssueUpdated(events.Event{WorkspaceID: "workspace-1", Payload: map[string]any{
		"issue": issue, "assignee_changed": false,
	}})
	if items := store.Snapshot(); len(items) != 0 {
		t.Fatalf("unchanged/former-member assignments should not enqueue: %+v", items)
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
