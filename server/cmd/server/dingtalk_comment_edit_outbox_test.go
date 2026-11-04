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
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDingTalkPersonalCommentEditOutbox(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	t.Setenv("MULTICA_APP_URL", "https://multica.example.test")
	issueID := fixture.Issue(t, "编辑通知任务")
	stamp := time.Now().UnixNano()
	users := make([]string, 3)
	for i := range users {
		users[i] = fixture.User(t, fmt.Sprintf("Edit mention user %d", i), fmt.Sprintf("edit-mention-%d-%d@example.test", stamp, i))
		fixture.Member(t, testWorkspaceID, users[i], "member")
		fixture.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
			"ding_user_id":    fmt.Sprintf("edit-mention-ding-%d-%d", stamp, i),
			"union_id":        fmt.Sprintf("edit-mention-union-%d-%d", stamp, i),
			"multica_user_id": users[i], "active": true, "login_only": false,
			"updated_at": testutil.Raw("now()"),
		}, "multica_user_id = $1", users[i])
	}
	senderID, dennyID, liID := users[0], users[1], users[2]
	commentID := uuid.NewString()
	fixture.Cleanup(t, `DELETE FROM dingtalk_personal_message WHERE comment_id = $1`, commentID)
	denny := "[@Denny](mention://member/" + dennyID + ")"
	li := "[@李群](mention://member/" + liID + ")"
	wakeup := &personalMentionWakeupRecorder{}
	runtime := &dingtalkNotifyRuntime{pool: testPool, personalWakeup: wakeup}
	bus := events.New()
	runtime.subscribeCommentEvents(bus)
	if bus.SubscriberCount(protocol.EventCommentUpdated) != 1 {
		t.Fatal("comment edits must have a personal notification subscriber")
	}
	comment := handler.CommentResponse{
		ID: commentID, IssueID: issueID, AuthorType: "member", AuthorID: senderID,
		Content: denny + " 方案 A", Revision: 1,
	}
	bus.Publish(events.Event{
		Type: protocol.EventCommentCreated, WorkspaceID: testWorkspaceID,
		ActorType: "member", ActorID: senderID, Payload: map[string]any{"comment": comment},
	})
	count := func(recipient string, want int) {
		t.Helper()
		got := fixture.Count(t, `SELECT count(*) FROM dingtalk_personal_message WHERE comment_id = $1 AND recipient_user_id = $2`, commentID, recipient)
		if got != want {
			t.Fatalf("recipient %s has %d messages, want %d", recipient, got, want)
		}
	}
	count(dennyID, 1)
	count(liID, 0)
	edit := func(before, after string, revision int64, withSnapshot bool) {
		t.Helper()
		comment.Content, comment.Revision = after, revision
		// The editor differs from the comment author; delivery must use the editor.
		comment.AuthorID = dennyID
		event := events.Event{
			Type: protocol.EventCommentUpdated, WorkspaceID: testWorkspaceID,
			ActorType: "member", ActorID: senderID, Payload: map[string]any{"comment": comment},
		}
		if withSnapshot {
			event.PreviousCommentContent = &before
		}
		bus.Publish(event)
	}
	original := denny + " 方案 A"
	added := denny + " " + li + " 方案 A " + li
	edit(original, added, 2, true)
	count(dennyID, 1)
	count(liID, 1)
	updated := denny + " " + li + " 方案 B\n保留完整的更新内容 " + li
	edit(added, updated, 3, true)
	edit(added, updated, 3, true) // At-least-once delivery of the same revision.
	count(dennyID, 2)
	count(liID, 2)

	key := fmt.Sprintf("dingtalk-personal-mention:%s:3:%s", commentID, liID)
	var actualSender, markdown string
	fixture.QueryRow(t, `SELECT sender_user_id::text, markdown FROM dingtalk_personal_message WHERE idempotency_key = $1`, key).Scan(&actualSender, &markdown)
	if actualSender != senderID {
		t.Fatalf("edit sent as %s, want editor %s", actualSender, senderID)
	}
	if !strings.HasPrefix(markdown, "**评论已更新**\n\n") || !strings.Contains(markdown, "保留完整的更新内容") {
		t.Fatalf("updated notification lost update label or body: %q", markdown)
	}
	if !strings.Contains(markdown, "编辑通知任务") || !strings.Contains(markdown, "https://multica.example.test/") || !strings.Contains(markdown, "#comment-"+commentID) {
		t.Fatalf("updated notification lost task context or comment link: %q", markdown)
	}

	removed := li + " 方案 B\n保留完整的更新内容 " + li
	edit(updated, removed, 4, true)
	count(dennyID, 2)
	count(liID, 2)
	latest := li + " 方案 C"
	edit(removed, latest, 5, true)
	count(dennyID, 2)
	count(liID, 3)
	edit(latest, latest, 5, true) // No-op saves do not notify.
	edit(latest, li+" unrelated attachment event", 6, false)
	edit(latest, "", 7, true) // Deleted-comment tombstone.
	count(dennyID, 2)
	count(liID, 3)
	if len(wakeup.users) != 4 {
		t.Fatalf("queue wakeups = %d, want one per notification-producing revision", len(wakeup.users))
	}
}
