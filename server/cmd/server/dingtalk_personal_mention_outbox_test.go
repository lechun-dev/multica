package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type personalMentionWakeupRecorder struct {
	users []string
}

func TestDingTalkPersonalAssignmentOutboxRoutesOperatorToNewAssignee(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	stamp := time.Now().UnixNano()
	actorID := fixture.User(t, "Assignment Actor", fmt.Sprintf("assignment-actor-%d@example.test", stamp))
	targetID := fixture.User(t, "Assignment Target", fmt.Sprintf("assignment-target-%d@example.test", stamp))
	fixture.Member(t, testWorkspaceID, actorID, "member")
	fixture.Member(t, testWorkspaceID, targetID, "member")
	actorDingID := fmt.Sprintf("assignment-actor-ding-%d", stamp)
	targetDingID := fmt.Sprintf("assignment-target-ding-%d", stamp)
	for _, identity := range []struct {
		userID string
		dingID string
	}{
		{userID: actorID, dingID: actorDingID},
		{userID: targetID, dingID: targetDingID},
	} {
		fixture.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
			"ding_user_id":    identity.dingID,
			"union_id":        "union-" + identity.dingID,
			"multica_user_id": identity.userID,
			"active":          true,
			"login_only":      false,
			"updated_at":      testutil.Raw("now()"),
		}, "multica_user_id = $1", identity.userID)
	}
	fixture.Cleanup(t, `DELETE FROM dingtalk_personal_message WHERE sender_user_id = $1`, actorID)

	wakeup := &personalMentionWakeupRecorder{}
	runtime := &dingtalkNotifyRuntime{pool: testPool, personalWakeup: wakeup}
	issue := dingtalkAssignmentIssue{
		id: "44444444-4444-4444-4444-444444444444", workspaceID: testWorkspaceID,
		identifier: "MUL-888", title: "转交消息测试", revision: 12,
	}
	event := notify.AssignmentNotification{
		WorkspaceID: testWorkspaceID, RecipientID: targetID, Kind: notify.AssignmentAdded,
		ActorName: "张畅", IssueIdentifier: issue.identifier, IssueTitle: issue.title,
		IncludeDetails: true, OccurredAt: time.Date(2026, 10, 13, 2, 30, 0, 0, time.UTC),
	}
	runtime.enqueuePersonalAssignmentNotification(context.Background(), issue, actorID, targetID, event)
	runtime.enqueuePersonalAssignmentNotification(context.Background(), issue, actorID, targetID, event)

	var senderDingID, recipientDingID, markdown, idempotencyKey string
	err := testPool.QueryRow(context.Background(), `
		SELECT sender_ding_user_id, recipient_ding_user_id, markdown, idempotency_key
		FROM dingtalk_personal_message
		WHERE sender_user_id = $1 AND recipient_user_id = $2 AND comment_id = $3`,
		actorID, targetID, issue.id).Scan(&senderDingID, &recipientDingID, &markdown, &idempotencyKey)
	if err != nil {
		t.Fatalf("load personal assignment outbox: %v", err)
	}
	if senderDingID != actorDingID || recipientDingID != targetDingID {
		t.Fatalf("personal assignment route sender=%q recipient=%q, want %q/%q", senderDingID, recipientDingID, actorDingID, targetDingID)
	}
	wantMarkdown := "你已成为该任务的负责人\n\n操作人：张畅\n操作时间：2026-10-13 10:30\n\n---\n\n来源：MUL-888 · 转交消息测试"
	if markdown != wantMarkdown {
		t.Fatalf("personal assignment markdown=%q, want %q", markdown, wantMarkdown)
	}
	wantKey := "dingtalk-personal-assignment:" + issue.id + ":12:" + targetID
	if idempotencyKey != wantKey {
		t.Fatalf("personal assignment idempotency key=%q, want %q", idempotencyKey, wantKey)
	}
	if count := fixture.Count(t, `SELECT count(*) FROM dingtalk_personal_message WHERE idempotency_key = $1`, wantKey); count != 1 {
		t.Fatalf("replayed personal assignment rows=%d, want 1", count)
	}
	if len(wakeup.users) != 1 || wakeup.users[0] != actorID {
		t.Fatalf("personal assignment wakeups=%v, want [%s]", wakeup.users, actorID)
	}
}

func (r *personalMentionWakeupRecorder) NotifyDingTalkPersonalMessageAvailable(userID string) {
	r.users = append(r.users, userID)
}

func TestDingTalkPersonalMentionOutboxCommentPolicy(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	stamp := time.Now().UnixNano()
	actorID := fixture.User(t, "Actor Test", fmt.Sprintf("personal-actor-%d@example.test", stamp))
	targetID := fixture.User(t, "Target Test", fmt.Sprintf("personal-target-%d@example.test", stamp))
	fixture.Member(t, testWorkspaceID, actorID, "member")
	fixture.Member(t, testWorkspaceID, targetID, "member")
	actorDingID := fmt.Sprintf("personal-actor-ding-%d", stamp)
	targetDingID := fmt.Sprintf("personal-target-ding-%d", stamp)
	fixture.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
		"ding_user_id":    actorDingID,
		"union_id":        "union-" + actorDingID,
		"multica_user_id": actorID,
		"active":          true,
		"login_only":      false,
		"updated_at":      testutil.Raw("now()"),
	}, "multica_user_id = $1", actorID)
	fixture.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
		"ding_user_id":    targetDingID,
		"union_id":        "union-" + targetDingID,
		"multica_user_id": targetID,
		"active":          true,
		"login_only":      false,
		"updated_at":      testutil.Raw("now()"),
	}, "multica_user_id = $1", targetID)
	fixture.Cleanup(t, `DELETE FROM dingtalk_personal_message WHERE sender_user_id = $1`, actorID)

	countForComment := func(commentID string) int {
		return fixture.Count(t, `SELECT count(*) FROM dingtalk_personal_message WHERE comment_id = $1`, commentID)
	}
	wakeup := &personalMentionWakeupRecorder{}
	runtime := &dingtalkNotifyRuntime{pool: testPool, personalWakeup: wakeup}
	emit := func(commentID, authorType, authorID, content string) {
		runtime.handleComment(events.Event{
			Type:        "comment:created",
			WorkspaceID: testWorkspaceID,
			ActorType:   authorType,
			ActorID:     authorID,
			Payload: map[string]any{"comment": handler.CommentResponse{
				ID: commentID, AuthorType: authorType, AuthorID: authorID, Content: content,
			}},
		})
	}

	memberCommentID := uuid.NewString()
	mention := "[@Target](mention://member/" + targetID + ")"
	memberContent := "第一行请确认 " + mention + "，重复 " + mention + "，这里补充足够长的内容用于验证私聊正文不再受四十字限制。\n第二行保留原始换行。\n第三行末尾仍然可见。"
	emit(memberCommentID, "member", actorID, memberContent)
	if count := countForComment(memberCommentID); count != 1 {
		t.Fatalf("member comment duplicate mentions created %d rows, want 1", count)
	}
	emit(memberCommentID, "member", actorID, memberContent)
	if count := countForComment(memberCommentID); count != 1 {
		t.Fatalf("replayed member comment created %d rows, want 1", count)
	}
	var senderDingID, recipientDingID, markdown string
	fixture.QueryRow(t, `
		SELECT sender_ding_user_id, recipient_ding_user_id, markdown
		FROM dingtalk_personal_message WHERE comment_id = $1`, memberCommentID).
		Scan(&senderDingID, &recipientDingID, &markdown)
	if senderDingID != actorDingID || recipientDingID != targetDingID {
		t.Fatalf("unexpected DingTalk route: sender=%q recipient=%q", senderDingID, recipientDingID)
	}
	if markdown == "" || markdown == mention {
		t.Fatalf("personal mention did not use the notification formatter: %q", markdown)
	}
	if strings.Count(markdown, "@Target") != 2 || strings.Contains(markdown, "mention://") {
		t.Fatalf("personal mention did not preserve readable mention names: %q", markdown)
	}
	if !strings.Contains(markdown, "第三行末尾仍然可见。") {
		t.Fatalf("personal mention truncated multiline content: %q", markdown)
	}
	if len(wakeup.users) != 1 || wakeup.users[0] != actorID {
		t.Fatalf("unexpected personal-message wakeups: %#v", wakeup.users)
	}

	selfCommentID := uuid.NewString()
	emit(selfCommentID, "member", actorID, "self [@Actor](mention://member/"+actorID+")")
	if count := countForComment(selfCommentID); count != 1 {
		t.Fatalf("self mention created %d rows, want 1", count)
	}
	fixture.QueryRow(t, `
		SELECT sender_ding_user_id, recipient_ding_user_id
		FROM dingtalk_personal_message WHERE comment_id = $1`, selfCommentID).
		Scan(&senderDingID, &recipientDingID)
	if senderDingID != actorDingID || recipientDingID != actorDingID {
		t.Fatalf("unexpected self DingTalk route: sender=%q recipient=%q", senderDingID, recipientDingID)
	}
	agentID := fixture.Agent(t, "Owned Test Agent", "", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"owner_id":     actorID,
		"kind":         "user",
	})
	agentCommentID := uuid.NewString()
	emit(agentCommentID, "agent", agentID, mention)
	if count := countForComment(agentCommentID); count != 1 {
		t.Fatalf("owned Agent-authored mention created %d rows, want 1", count)
	}
	fixture.QueryRow(t, `
		SELECT sender_ding_user_id, recipient_ding_user_id, markdown
		FROM dingtalk_personal_message WHERE comment_id = $1`, agentCommentID).
		Scan(&senderDingID, &recipientDingID, &markdown)
	if senderDingID != actorDingID || recipientDingID != targetDingID {
		t.Fatalf("owned Agent did not route through its owner: sender=%q recipient=%q", senderDingID, recipientDingID)
	}
	if !strings.Contains(markdown, "你的 Agent") {
		t.Fatalf("owned Agent personal message did not disclose Agent authorship: %q", markdown)
	}
	if len(wakeup.users) != 3 || wakeup.users[2] != actorID {
		t.Fatalf("owned Agent did not wake its owner's queue: %#v", wakeup.users)
	}
	systemCommentID := uuid.NewString()
	emit(systemCommentID, "system", actorID, mention)
	if count := countForComment(systemCommentID); count != 0 {
		t.Fatalf("system-authored mention created %d rows", count)
	}
	outsiderID := fixture.User(t, "Outside Target", fmt.Sprintf("personal-outsider-%d@example.test", stamp))
	outsiderDingID := fmt.Sprintf("personal-outsider-ding-%d", stamp)
	fixture.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
		"ding_user_id":    outsiderDingID,
		"union_id":        "union-" + outsiderDingID,
		"multica_user_id": outsiderID,
		"active":          true,
		"login_only":      false,
		"updated_at":      testutil.Raw("now()"),
	}, "multica_user_id = $1", outsiderID)
	outsideCommentID := uuid.NewString()
	emit(outsideCommentID, "member", actorID, "forged [@Outside](mention://member/"+outsiderID+")")
	if count := countForComment(outsideCommentID); count != 0 {
		t.Fatalf("out-of-workspace mention created %d rows", count)
	}

	fixture.InsertNoID(t, "notification_preference", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"user_id":      actorID,
		"preferences":  testutil.Raw(`'{"dingtalk_personal_mentions":"muted"}'::jsonb`),
	}, "workspace_id = $1 AND user_id = $2", testWorkspaceID, actorID)
	mutedCommentID := uuid.NewString()
	emit(mutedCommentID, "member", actorID, mention)
	if count := countForComment(mutedCommentID); count != 0 {
		t.Fatalf("muted personal mention created %d rows", count)
	}
}
