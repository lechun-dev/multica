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
	if got := messages[0].Text; got != "✅ 智能体「销售分析」已完成执行" || strings.Contains(got, "提到了你") {
		t.Fatalf("completion text=%q", got)
	}
}

func TestFormatAgentCompletionTextIncludesOptionalResultLink(t *testing.T) {
	got := FormatAgentCompletionText(AgentCompleted{AgentName: "A*gent", SourceURL: "https://multica.test/task-1"})
	want := "✅ 智能体「A\\*gent」已完成执行\n\n**[查看执行结果](https://multica.test/task-1)**"
	if got != want {
		t.Fatalf("formatted completion = %q, want %q", got, want)
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
	want := "🔔 **张畅 在 Multica 中提到了你**\n\n***来源：乐纯工作区 / 钉钉通知***\n\n***任务：[MUL-67 · 优化成员通知](https://multica.lechun.cc/acme/issues/MUL-67#comment-comment-1)***\n\n> 请 @李群 周五前确认\n\n**[打开任务并回复](https://multica.lechun.cc/acme/issues/MUL-67#comment-comment-1)**"
	if got != want {
		t.Fatalf("formatted notification = %q, want %q", got, want)
	}
}

func TestFormatTextStripsInternalMentionLinksButKeepsRegularLinks(t *testing.T) {
	got := FormatText(MentionCreated{Text: "请 [@Agent](mention://agent/agent-id) 查看 [文档](https://example.test/doc)"})
	want := "🔔 **一位 Multica 成员 在 Multica 中提到了你**\n\n> 请 @Agent 查看 [文档](https://example.test/doc)"
	if got != want {
		t.Fatalf("formatted notification = %q, want %q", got, want)
	}
}

func TestFormatTextDoesNotExposeActorIDWhenNameMissing(t *testing.T) {
	got := FormatText(MentionCreated{Actor: Actor{ID: "secret-user-id", Kind: "member"}, Text: "hello"})
	if got != "🔔 **一位 Multica 成员 在 Multica 中提到了你**\n\n> hello" {
		t.Fatalf("formatted notification exposed an opaque actor id or changed fallback: %q", got)
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
	if got != "🔔 **A\\*lice 在 Multica 中提到了你**\n\n***来源：Acme\\_\\[研发\\]***\n\n***任务：MUL-1 · Fix \\`notify\\`***\n\n> done" {
		t.Fatalf("formatted notification did not escape display fields: %q", got)
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
