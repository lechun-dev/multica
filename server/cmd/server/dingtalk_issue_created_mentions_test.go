package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDingTalkIssueCreatedDescriptionOutbox(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name                                     string
		personalOnly, muted, restricted, unbound bool
		unassigned, selfOnly                     bool
	}{
		{name: "both channels and merged assignee"},
		{name: "personal without robot configuration", personalOnly: true},
		{name: "muted personal mentions retain assignment", muted: true},
		{name: "no issue access", restricted: true},
		{name: "identities cannot receive", unbound: true},
		{name: "unassigned task", unassigned: true},
		{name: "self and nonmember mentions only", selfOnly: true, unassigned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := testutil.New(testPool, testWorkspaceID, testUserID)
			t.Setenv("MULTICA_APP_URL", "https://multica.example.test")
			issueID := fixture.Issue(t, "创建正文通知任务")
			fixture.Cleanup(t, `DELETE FROM dingtalk_personal_message WHERE comment_id = $1`, issueID)
			stamp := time.Now().UnixNano()
			users := make([]string, 5)
			bindings := make(map[string]notify.MemberBinding)
			for i := range users {
				users[i] = fixture.User(t, fmt.Sprintf("Creation user %d", i), fmt.Sprintf("created-mention-%d-%d@example.test", stamp, i))
				// One user has a valid identity but is outside the workspace.
				if i != 3 {
					fixture.Member(t, testWorkspaceID, users[i], "member")
				}
				dingID := fmt.Sprintf("created-ding-%d-%d", stamp, i)
				fixture.InsertNoID(t, "dingtalk_notify_identities", testutil.Cols{
					"ding_user_id": dingID, "union_id": fmt.Sprintf("created-union-%d-%d", stamp, i),
					"multica_user_id": users[i], "active": true, "login_only": tc.unbound && i > 0,
					"updated_at": testutil.Raw("now()"),
				}, "multica_user_id = $1", users[i])
				bindings[users[i]] = notify.MemberBinding{
					DingUserID: dingID, Active: !tc.unbound || i == 0,
					Groups: []notify.AgentChannel{{ChannelID: "must-not-send-to-group", ChannelName: "项目群", Active: true}},
				}
			}
			actorID, assigneeID, mentionedID, outsiderID := users[0], users[1], users[2], users[3]
			if tc.muted {
				fixture.InsertNoID(t, "notification_preference", testutil.Cols{
					"workspace_id": testWorkspaceID, "user_id": actorID,
					"preferences": testutil.Raw(`'{"dingtalk_personal_mentions":"muted"}'::jsonb`),
				}, "workspace_id = $1 AND user_id = $2", testWorkspaceID, actorID)
			}
			mention := func(id string) string { return "[@用户](mention://member/" + id + ")" }
			body := mention(actorID) + mention(outsiderID) + " [@所有人](mention://all/all) [@Agent](mention://agent/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa) [@团队](mention://squad/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb)"
			if !tc.selfOnly {
				body += mention(assigneeID) + mention(mentionedID) + mention(mentionedID)
			}
			body += " 请在项目群里确认\n完整正文第二段\n完整正文第三段"
			store := notify.NewMemoryStore()
			wakeup := &personalMentionWakeupRecorder{}
			runtime := &dingtalkNotifyRuntime{
				pool: testPool, personalWakeup: wakeup, memberInWorkspace: dingtalkWorkspaceMemberChecker(testPool),
				canViewIssue: func(context.Context, string, string, string) (bool, error) { return !tc.restricted, nil },
			}
			if !tc.personalOnly {
				runtime.store, runtime.resolver = store, dingtalkNotifyResolverStub{bindings: bindings}
			}
			memberType := "member"
			issue := handler.IssueResponse{ID: issueID, WorkspaceID: testWorkspaceID, Identifier: "LC-999", Title: "创建正文通知任务", Description: &body, Revision: 1}
			if !tc.unassigned {
				issue.AssigneeType, issue.AssigneeID = &memberType, &assigneeID
			}
			event := events.Event{Type: protocol.EventIssueCreated, WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: actorID,
				Payload: map[string]any{"issue": issue}}
			runtime.handleIssueCreated(event)
			// Serialized replay has a different revision and assignment state. The
			// creation key must still deduplicate each recipient across both paths.
			event.Payload = map[string]any{"issue": map[string]any{
				"id": issueID, "workspace_id": testWorkspaceID, "description": body, "revision": float64(99),
			}}
			runtime.handleIssueCreated(event)
			// Autosaves (including newly added mentions) never invoke this path.
			edited := body + mention(users[4]) + " 新增的正文和接收人"
			issue.Description, issue.Revision = &edited, 100
			event.Type = protocol.EventIssueUpdated
			event.Payload = map[string]any{"issue": issue, "description_changed": true, "assignee_changed": false}
			runtime.handleIssueUpdated(event)

			wantRobot, wantPersonal := 2, 2
			if tc.selfOnly {
				wantRobot, wantPersonal = 0, 0
			}
			if tc.restricted {
				wantRobot, wantPersonal = 1, 1
			}
			if tc.muted {
				wantPersonal = 1
			}
			if tc.unbound {
				wantRobot, wantPersonal = 0, 0
			}
			if tc.personalOnly {
				wantRobot = 0
			}
			items := store.Snapshot()
			if len(items) != wantRobot {
				t.Fatalf("robot messages = %d, want %d: %+v", len(items), wantRobot, items)
			}
			for _, item := range items {
				if item.Message.ChannelType != "p2p" || (item.Message.TargetID != assigneeID && item.Message.TargetID != mentionedID) {
					t.Fatalf("unexpected robot destination: %+v", item.Message)
				}
				if tc.restricted && strings.Contains(item.Message.Text, "正文") {
					t.Fatalf("restricted robot message leaked body: %q", item.Message.Text)
				}
				if !tc.unassigned && !tc.restricted && item.Message.TargetID == assigneeID && !strings.Contains(item.Message.Text, "你也在任务正文中被提及") {
					t.Fatalf("merged robot message lost mention: %q", item.Message.Text)
				}
			}
			got := fixture.Count(t, `SELECT count(*) FROM dingtalk_personal_message WHERE comment_id = $1`, issueID)
			if got != wantPersonal {
				t.Fatalf("personal messages = %d, want %d", got, wantPersonal)
			}
			for _, recipient := range []string{assigneeID, mentionedID} {
				var markdown string
				rows := fixture.Count(t, `SELECT count(*) FROM dingtalk_personal_message WHERE comment_id = $1 AND recipient_user_id = $2`, issueID, recipient)
				if rows > 1 {
					t.Fatalf("recipient %s received %d messages", recipient, rows)
				}
				if rows == 0 {
					continue
				}
				fixture.QueryRow(t, `SELECT markdown FROM dingtalk_personal_message WHERE comment_id = $1 AND recipient_user_id = $2`, issueID, recipient).Scan(&markdown)
				if tc.restricted || tc.muted {
					if strings.Contains(markdown, "完整正文") || strings.Contains(markdown, "你也在任务正文中被提及") {
						t.Fatalf("minimal/muted assignment leaked mention: %q", markdown)
					}
					continue
				}
				if !strings.Contains(markdown, "完整正文第三段") || !strings.Contains(markdown, "https://multica.example.test/") || strings.Contains(markdown, "#comment-") || strings.Contains(markdown, "mention://") {
					t.Fatalf("personal message lost body/task link or retained routing targets: %q", markdown)
				}
				if recipient == assigneeID && !tc.unassigned && !strings.Contains(markdown, "你也在任务正文中被提及") {
					t.Fatalf("merged personal message lost mention: %q", markdown)
				}
			}
			if wantPersonal > 0 && len(wakeup.users) == 0 {
				t.Fatal("personal queue was not woken")
			}
		})
	}
}

func TestDingTalkIssueCreatedDoesNotReloadEditedDescription(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	issueID := fixture.Issue(t, "later edited body")
	const memberID = "11111111-1111-1111-1111-111111111111"
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET description = $2, revision = 20 WHERE id = $1`, issueID, "[@Later](mention://member/"+memberID+")"); err != nil {
		t.Fatal(err)
	}
	store := notify.NewMemoryStore()
	runtime := &dingtalkNotifyRuntime{pool: testPool, store: store,
		resolver:          dingtalkNotifyResolverStub{bindings: map[string]notify.MemberBinding{memberID: {DingUserID: "ding-later", Active: true}}},
		memberInWorkspace: func(context.Context, string, string) (bool, error) { return true, nil },
		canViewIssue:      func(context.Context, string, string, string) (bool, error) { return true, nil },
	}
	runtime.handleIssueCreated(events.Event{WorkspaceID: testWorkspaceID, Payload: map[string]any{"issue_id": issueID}})
	if items := store.Snapshot(); len(items) != 0 {
		t.Fatalf("minimal event notified a later edit: %+v", items)
	}
}
