package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type personalMentionWakeupRecorder struct {
	users []string
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
	emit(memberCommentID, "member", actorID, "请确认 "+mention+"，重复 "+mention)
	if count := countForComment(memberCommentID); count != 1 {
		t.Fatalf("member comment duplicate mentions created %d rows, want 1", count)
	}
	emit(memberCommentID, "member", actorID, "请确认 "+mention+"，重复 "+mention)
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
	if strings.Contains(markdown, "@Target") || strings.Contains(markdown, "mention://") {
		t.Fatalf("personal mention exposed routing markup: %q", markdown)
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
	agentCommentID := uuid.NewString()
	emit(agentCommentID, "agent", actorID, mention)
	if count := countForComment(agentCommentID); count != 0 {
		t.Fatalf("Agent-authored mention created %d rows", count)
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
