package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type resolverStub struct {
	member   MemberBinding
	found    bool
	channels []AgentChannel
}

func (s resolverStub) MemberBinding(context.Context, string, string) (MemberBinding, bool, error) {
	return s.member, s.found, nil
}
func (s resolverStub) AgentChannels(context.Context, string, string) ([]AgentChannel, error) {
	return s.channels, nil
}

type providerStub struct {
	sent []Message
	err  error
}

func (s *providerStub) Send(_ context.Context, message Message) error {
	s.sent = append(s.sent, message)
	return s.err
}

func TestBuildMessagesRoutesMemberToP2PAndDefersAgentByDefault(t *testing.T) {
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Actor: Actor{Name: "Alice"}, Text: "请发到互联网中心", SourceURL: "https://multica.test/i/1", Targets: []MentionTarget{{ID: "m1", Kind: "member"}, {ID: "a1", Kind: "agent"}}}
	messages, failures, err := BuildMessages(context.Background(), event, resolverStub{member: MemberBinding{DingUserID: "d1", Active: true}, found: true, channels: []AgentChannel{{AgentID: "a1", ChannelID: "c1", ChannelName: "互联网中心", Active: true}, {AgentID: "a1", ChannelID: "c2", ChannelName: "另一个群", Active: true}}})
	if err != nil || len(failures) != 1 || failures[0].Status != StatusSkipped || len(messages) != 1 {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
	if messages[0].DingUserID != "d1" {
		t.Fatalf("unexpected routing: %+v", messages)
	}
}

func TestBuildMessagesAgentRequiresExplicitFeatureAndOwnRobot(t *testing.T) {
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Actor: Actor{Name: "Alice"}, Text: "请发到互联网中心", Targets: []MentionTarget{{ID: "a1", Kind: "agent"}}}
	resolver := resolverStub{channels: []AgentChannel{{AgentID: "a1", ChannelID: "c1", ChannelName: "互联网中心", Active: true}}}
	messages, failures, err := BuildMessagesWithOptions(context.Background(), event, resolver, RoutingOptions{EnableAgentNotifications: true})
	if err != nil || len(messages) != 0 || len(failures) != 1 || failures[0].Status != "failed" {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
	resolver.channels[0].RobotCode = "robot-a"
	messages, failures, err = BuildMessagesWithOptions(context.Background(), event, resolver, RoutingOptions{EnableAgentNotifications: true})
	if err != nil || len(failures) != 0 || len(messages) != 1 || messages[0].RobotCode != "robot-a" {
		t.Fatalf("configured agent routing messages=%+v failures=%+v err=%v", messages, failures, err)
	}
}

func TestBuildMessagesUsesMemberGroupsOnlyForExplicitGroupIntent(t *testing.T) {
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Actor: Actor{Name: "Alice"}, Text: "请发群给大家", Targets: []MentionTarget{{ID: "m1", Kind: "member"}}}
	messages, failures, err := BuildMessages(context.Background(), event, resolverStub{member: MemberBinding{DingUserID: "d1", Active: true, Groups: []AgentChannel{{ChannelID: "g1", ChannelName: "项目群", Active: true}}}, found: true})
	if err != nil || len(failures) != 0 || len(messages) != 1 || messages[0].ChannelType != "group" {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
}

func TestBuildMessagesUsesNamedMemberGroupOnly(t *testing.T) {
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Actor: Actor{Name: "Alice"}, Text: "请发到项目群", Targets: []MentionTarget{{ID: "m1", Kind: "member"}}}
	messages, failures, err := BuildMessages(context.Background(), event, resolverStub{member: MemberBinding{DingUserID: "d1", Active: true, Groups: []AgentChannel{{ChannelID: "g1", ChannelName: "项目群", Active: true}, {ChannelID: "g2", ChannelName: "其他群", Active: true}}}, found: true})
	if err != nil || len(failures) != 0 || len(messages) != 1 || messages[0].ChannelID != "g1" {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
}

func TestBuildMessagesMarksUnboundAndDoesNotNotifyAgentOwner(t *testing.T) {
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Actor: Actor{Name: "Alice"}, Text: "普通通知", Targets: []MentionTarget{{ID: "m1", Kind: "member"}, {ID: "a1", Kind: "agent"}}}
	messages, failures, err := BuildMessages(context.Background(), event, resolverStub{found: false, channels: []AgentChannel{{AgentID: "a1", ChannelID: "c1", ChannelName: "固定群", Active: true}}})
	if err != nil || len(messages) != 0 || len(failures) != 2 || failures[1].Status != StatusSkipped {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
}

func TestBuildCompletionMessagesNotifiesOwnerAndInitiatorOnce(t *testing.T) {
	const recipient = "member-1"
	event := AgentCompleted{EventID: "task-1", WorkspaceID: "workspace-1", AgentID: "agent-1", AgentName: "销售分析"}
	messages, failures, err := BuildCompletionMessages(context.Background(), event, []string{recipient, recipient}, resolverStub{
		member: MemberBinding{DingUserID: "ding-user-1", Active: true}, found: true,
	})
	if err != nil || len(failures) != 0 || len(messages) != 1 {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
	if messages[0].ChannelType != "p2p" || messages[0].DingUserID != "ding-user-1" {
		t.Fatalf("unexpected completion routing: %+v", messages[0])
	}
	if got := messages[0].Text; got != "✅ **销售分析 已完成执行**" || strings.Contains(got, "提到了你") {
		t.Fatalf("completion text=%q", got)
	}
}

func TestBuildAssignmentMessageRoutesMemberToP2P(t *testing.T) {
	event := AssignmentNotification{
		EventID: "issue-assignment:issue-1:7:assignment_added", WorkspaceID: "workspace-1",
		RecipientID: "member-1", Kind: AssignmentAdded, IncludeDetails: true,
		ActorName: "张畅", OccurredAt: time.Date(2026, 10, 13, 2, 30, 0, 0, time.UTC),
		IssueIdentifier: "MUL-123", IssueTitle: "优化手机端登录", SourceURL: "https://multica.test/issues/MUL-123",
	}
	messages, failures, err := BuildAssignmentMessage(context.Background(), event, resolverStub{
		member: MemberBinding{DingUserID: "ding-user-1", RobotCode: "mission-robot", Active: true}, found: true,
	})
	if err != nil || len(failures) != 0 || len(messages) != 1 {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
	message := messages[0]
	if message.TargetID != "member-1" || message.TargetKind != "member" || message.DingUserID != "ding-user-1" || message.ChannelType != "p2p" {
		t.Fatalf("unexpected assignment routing: %+v", message)
	}
	want := "📌 **你已成为该任务的负责人**\n\n操作人：张畅\n\n操作时间：2026-10-13 10:30\n\n---\n\n来源：MUL-123 · 优化手机端登录\n\n[打开任务](https://multica.test/issues/MUL-123)"
	if message.Text != want {
		t.Fatalf("assignment text=%q, want %q", message.Text, want)
	}
}

func TestBuildAssignmentMessageReportsUnboundMember(t *testing.T) {
	event := AssignmentNotification{EventID: "assignment-1", WorkspaceID: "workspace-1", RecipientID: "member-1", Kind: AssignmentRemoved}
	messages, failures, err := BuildAssignmentMessage(context.Background(), event, resolverStub{})
	if err != nil || len(messages) != 0 || len(failures) != 1 || failures[0].Status != StatusFailed {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
}

func TestBuildAssignmentMessageRejectsUnsupportedKind(t *testing.T) {
	event := AssignmentNotification{EventID: "assignment-1", WorkspaceID: "workspace-1", RecipientID: "member-1", Kind: "unknown"}
	if _, _, err := BuildAssignmentMessage(context.Background(), event, resolverStub{}); err == nil {
		t.Fatal("unsupported assignment kind should fail")
	}
}

func TestFormatAssignmentTextUsesOnlyApprovedMessageTypes(t *testing.T) {
	if got := FormatAssignmentText(AssignmentNotification{Kind: AssignmentAdded}); got != "📌 **你已成为该任务的负责人**" {
		t.Fatalf("assigned text=%q", got)
	}
	if got := FormatAssignmentText(AssignmentNotification{Kind: AssignmentRemoved, NewAssigneeName: "李群"}); got != "↗️ **该任务已转交给 李群**" {
		t.Fatalf("removed text=%q", got)
	}
	got := FormatAssignmentText(AssignmentNotification{
		Kind: AssignmentRemoved, NewAssigneeName: "李群", ActorName: "张畅",
		OccurredAt: time.Date(2026, 10, 13, 2, 30, 0, 0, time.UTC), IncludeDetails: false,
		IssueIdentifier: "MUL-123", IssueTitle: "不应泄露", SourceURL: "https://multica.test/issues/MUL-123",
	})
	if got != "↗️ **该任务已转交给 李群**\n\n操作人：张畅\n\n操作时间：2026-10-13 10:30" {
		t.Fatalf("minimal removed text leaked task details: %q", got)
	}
}

func TestFormatPersonalAssignmentTextMatchesTransferCopy(t *testing.T) {
	got := FormatPersonalAssignmentText(AssignmentNotification{
		Kind: AssignmentAdded, ActorName: "张畅",
		OccurredAt: time.Date(2026, 10, 13, 2, 30, 0, 0, time.UTC), IncludeDetails: true,
		IssueIdentifier: "LC-71", IssueTitle: "子任务测试", SourceURL: "https://multica.test/issues/LC-71",
	})
	want := "你已成为该任务的负责人\n\n操作人：张畅\n\n操作时间：2026-10-13 10:30\n\n---\n\n来源：LC-71 · 子任务测试\n\n[打开任务](https://multica.test/issues/LC-71)"
	if got != want {
		t.Fatalf("personal assignment text=%q, want %q", got, want)
	}
}

func TestFormatAgentCompletionTextIncludesOptionalResultLink(t *testing.T) {
	got := FormatAgentCompletionText(AgentCompleted{AgentName: "A*gent", ResultText: "已完成通知流程修复。\n请查看执行结果。", WorkspaceName: "乐纯工作区", ProjectName: "钉钉通知", IssueIdentifier: "MUL-67", IssueTitle: "优化成员通知", SourceURL: "https://multica.test/task-1"})
	want := "✅ **A\\*gent 已完成执行**\n\n已完成通知流程修复。\n请查看执行结果。\n\n---\n\n来源：[MUL-67 · 优化成员通知](https://multica.test/task-1)\n\n[打开任务并回复](https://multica.test/task-1)"
	if got != want {
		t.Fatalf("formatted completion = %q, want %q", got, want)
	}
}

func TestFormatAgentCompletionTextBoundsResultPreviewAndHidesStructuredMentions(t *testing.T) {
	got := FormatAgentCompletionText(AgentCompleted{
		AgentName:  "测试智能体",
		ResultText: "[@李群](mention://member/member-1) 已完成第一部分，并补充了一段明显超过四十个字符的执行结果用于验证通知摘要不会展示全部正文内容。\n第二行也不会突破总字符上限。\n第三行不应展示。",
	})
	if strings.Contains(got, "@李群") || strings.Contains(got, "mention://") {
		t.Fatalf("completion preview leaked structured mention: %q", got)
	}
	if !strings.Contains(got, "…\n\n---") {
		t.Fatalf("completion preview was not bounded before divider: %q", got)
	}
}

func TestDeliverContinuesAfterFailure(t *testing.T) {
	provider := &providerStub{err: errors.New("rate limited")}
	results := Deliver(context.Background(), provider, []Message{{EventID: "e1"}, {EventID: "e2"}})
	if len(results) != 2 || results[0].Status != "failed" || len(provider.sent) != 2 {
		t.Fatalf("results=%+v sent=%d", results, len(provider.sent))
	}
}

func TestFormatTextIncludesReadableContextAndReplyLink(t *testing.T) {
	event := MentionCreated{
		Actor:           Actor{ID: "user-id", Name: "张畅", Kind: "member"},
		Text:            "请 [@李群](mention://member/member-id) 周五前确认",
		WorkspaceName:   "乐纯工作区",
		ProjectName:     "钉钉通知",
		IssueIdentifier: "MUL-67",
		IssueTitle:      "优化成员通知",
		SourceURL:       "https://multica.lechun.cc/acme/issues/MUL-67#comment-comment-1",
	}

	got := FormatText(event)
	want := "🔔 **张畅提到了你**\n\n请 周五前确认\n\n---\n\n来源：[MUL-67 · 优化成员通知](https://multica.lechun.cc/acme/issues/MUL-67#comment-comment-1)\n\n[打开任务并回复](https://multica.lechun.cc/acme/issues/MUL-67#comment-comment-1)"
	if got != want {
		t.Fatalf("formatted notification = %q, want %q", got, want)
	}
}

func TestFormatTextStripsInternalMentionLinksButKeepsRegularLinks(t *testing.T) {
	got := FormatText(MentionCreated{Text: "请 [@Agent](mention://agent/agent-id) 查看 [文档](https://example.test/doc)"})
	want := "🔔 **一位 MissionOS 成员提到了你**\n\n请 查看 [文档](https://example.test/doc)"
	if got != want {
		t.Fatalf("formatted notification = %q, want %q", got, want)
	}
}

func TestFormatTextDoesNotExposeActorIDWhenNameMissing(t *testing.T) {
	got := FormatText(MentionCreated{Actor: Actor{ID: "secret-user-id", Kind: "member"}, Text: "hello"})
	if got != "🔔 **一位 MissionOS 成员提到了你**\n\nhello" {
		t.Fatalf("formatted notification exposed an opaque actor id or changed fallback: %q", got)
	}
}

func TestFormatTextTruncatesLongMentionPreview(t *testing.T) {
	got := FormatText(MentionCreated{Actor: Actor{Name: "张畅"}, Text: "第一行\n第二行\n第三行\n第四行\n第五行\n第六行"})
	if strings.Contains(got, "第三行") || strings.Contains(got, "第四行") {
		t.Fatalf("long mention preview leaked lines beyond the compact limit: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("long mention preview should end with an ellipsis: %q", got)
	}
}

func TestFormatTextTruncatesLongSingleLineByRunes(t *testing.T) {
	text := strings.Repeat("任务内容", 200)
	got := FormatText(MentionCreated{Text: text})
	if !strings.Contains(got, "…") {
		t.Fatalf("long single-line mention preview should be truncated: %q", got)
	}
	if strings.Contains(got, "Multica") {
		t.Fatalf("legacy brand leaked into notification: %q", got)
	}
	preview := truncateMentionPreview(text)
	if gotRunes := len([]rune(strings.TrimSuffix(preview, "…"))); gotRunes != mentionPreviewMaxRunes {
		t.Fatalf("truncated preview has %d runes, want %d: %q", gotRunes, mentionPreviewMaxRunes, preview)
	}
}

func TestFormatTextEscapesDisplayContext(t *testing.T) {
	got := FormatText(MentionCreated{
		Actor:           Actor{Name: "A*lice", Kind: "member"},
		WorkspaceName:   "Acme_[研发]",
		IssueIdentifier: "MUL-1",
		IssueTitle:      "Fix `notify`",
		Text:            "done",
	})
	if got != "🔔 **A\\*lice提到了你**\n\ndone\n\n---\n\n来源：MUL-1 · Fix \\`notify\\`" {
		t.Fatalf("formatted notification did not escape display fields: %q", got)
	}
}

func TestFormatPersonalMentionTextPreservesMentionNamesAndKeepsSourceAtEnd(t *testing.T) {
	got := FormatPersonalMentionText(MentionCreated{
		Actor:           Actor{Name: "张畅", Kind: "member"},
		Text:            "[@张畅](mention://member/member-a) [@李群](mention://member/member-b) 测试私发消息，看看能不能收到",
		WorkspaceName:   "lechun-test",
		IssueIdentifier: "LECH-15",
		IssueTitle:      "连接运行时，和 Mika 开始",
		SourceURL:       "https://multica.test/issues/LECH-15",
	})
	want := "**@张畅** **@李群** 测试私发消息，看看能不能收到\n\n---\n\n[打开任务并回复](https://multica.test/issues/LECH-15)（来源：[LECH-15 · 连接运行时，和 Mika 开始](https://multica.test/issues/LECH-15)）"
	if got != want {
		t.Fatalf("formatted personal notification = %q, want %q", got, want)
	}
	for _, hidden := range []string{"mention://", "MissionOS 中提到了你"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("personal notification exposed %q: %q", hidden, got)
		}
	}
}

func TestFormatPersonalMentionTextPreservesMentionOnlyComment(t *testing.T) {
	got := FormatPersonalMentionText(MentionCreated{Text: "[@张畅](mention://member/member-a) [@李群](mention://member/member-b)"})
	if got != "**@张畅** **@李群**" {
		t.Fatalf("personal mention-only body = %q", got)
	}
}

func TestFormatPersonalMentionTextHandlesPartialFooterContext(t *testing.T) {
	if got := FormatPersonalMentionText(MentionCreated{Text: "hello", WorkspaceName: "lechun-test", ProjectName: "project"}); got != "hello" {
		t.Fatalf("workspace and project should stay hidden without a task: %q", got)
	}
	if got := FormatPersonalMentionText(MentionCreated{Text: "hello", SourceURL: "https://multica.test/task"}); got != "hello\n\n---\n\n[打开任务并回复](https://multica.test/task)" {
		t.Fatalf("link-only personal footer = %q", got)
	}
}

func TestFormatPersonalMentionTextPreservesCompleteBodyForAgent(t *testing.T) {
	got := FormatPersonalMentionText(MentionCreated{
		Actor:           Actor{Name: "Mika", Kind: "agent"},
		Text:            "[@李群](mention://member/member-b) 请检查结果",
		WorkspaceName:   "乐纯「私有」",
		ProjectName:     "迁移临时项目",
		IssueIdentifier: "LC-71",
		IssueTitle:      "子任务测试",
		SourceURL:       "https://multica.test/issues/LC-71",
	})
	want := "**@李群** 请检查结果\n\n---\n\n[打开任务并回复](https://multica.test/issues/LC-71)（来源：[LC-71 · 子任务测试](https://multica.test/issues/LC-71)）"
	if got != want {
		t.Fatalf("formatted Agent personal notification = %q, want %q", got, want)
	}
	for _, hidden := range []string{"乐纯", "迁移临时项目", "mention://"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("Agent personal notification exposed %q: %q", hidden, got)
		}
	}
}

func TestFormatPersonalMentionTextKeepsLongMultilineBody(t *testing.T) {
	body := "第一段内容超过原先四十字限制，用于确认钉钉私聊消息不会再被截断，并且保留完整正文。\n第二段保持原来的换行。\n第三段也必须完整展示。"
	got := FormatPersonalMentionText(MentionCreated{Text: body})
	if got != body {
		t.Fatalf("long multiline personal notification = %q, want %q", got, body)
	}
}

func TestFormatPersonalMentionTextOnlyBoldsStructuredAtMentions(t *testing.T) {
	body := "请 [@张畅](mention://member/member-a) 和 [MUL-1](mention://issue/issue-a) 确认 user@example.com、普通 @李群 以及 [外链](https://example.com)。"
	got := FormatPersonalMentionText(MentionCreated{Text: body})
	want := "请 **@张畅** 和 MUL-1 确认 user@example.com、普通 @李群 以及 [外链](https://example.com)。"
	if got != want {
		t.Fatalf("personal mention formatting=%q, want %q", got, want)
	}
}

func TestConfigAutomaticallyEnablesOnlyWithProviderCredentials(t *testing.T) {
	config := Config{DingTalkClientID: "app", DingTalkClientSecret: "secret", DingTalkRobotCode: "robot"}
	if err := config.Validate(); err != nil {
		t.Fatalf("configured notification provider should validate: %v", err)
	}
	config.DingTalkRobotCode = "CHANGE_ME"
	if err := config.Validate(); err == nil {
		t.Fatal("placeholder provider credentials must be rejected")
	}
}

func TestConfigFromEnvUsesInjectedValues(t *testing.T) {
	values := map[string]string{
		"DINGTALK_CLIENT_ID":              "app",
		"DINGTALK_CLIENT_SECRET":          "secret",
		"DINGTALK_ROBOT_CODE":             "robot",
		"DINGTALK_NOTIFY_WORKER_INTERVAL": "3s",
		"DINGTALK_NOTIFY_MAX_ATTEMPTS":    "7",
	}
	config := ConfigFromEnv(func(key string) string { return values[key] })
	if config.DingTalkClientID != "app" || config.WorkerInterval != 3*time.Second || config.MaxAttempts != 7 || !config.AgentOwnerMentions {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestConfigFromEnvCanDisableAgentOwnerMentions(t *testing.T) {
	config := ConfigFromEnv(func(key string) string {
		if key == "DINGTALK_NOTIFY_AGENT_OWNER_MENTIONS" {
			return "false"
		}
		return ""
	})
	if config.AgentOwnerMentions {
		t.Fatal("agent owner mentions should be disabled")
	}
}
