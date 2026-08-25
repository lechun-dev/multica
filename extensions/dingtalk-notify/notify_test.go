package notify

import (
	"context"
	"errors"
	"testing"
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

func TestBuildMessagesRoutesMemberToP2PAndAgentToNamedChannel(t *testing.T) {
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Actor: Actor{Name: "Alice"}, Text: "请发到互联网中心", SourceURL: "https://multica.test/i/1", Targets: []MentionTarget{{ID: "m1", Kind: "member"}, {ID: "a1", Kind: "agent"}}}
	messages, failures, err := BuildMessages(context.Background(), event, resolverStub{member: MemberBinding{DingUserID: "d1", Active: true}, found: true, channels: []AgentChannel{{AgentID: "a1", ChannelID: "c1", ChannelName: "互联网中心", Active: true}, {AgentID: "a1", ChannelID: "c2", ChannelName: "另一个群", Active: true}}})
	if err != nil || len(failures) != 0 || len(messages) != 2 {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
	if messages[0].DingUserID != "d1" || messages[1].ChannelID != "c1" {
		t.Fatalf("unexpected routing: %+v", messages)
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
	if err != nil || len(messages) != 0 || len(failures) != 2 {
		t.Fatalf("messages=%+v failures=%+v err=%v", messages, failures, err)
	}
}

func TestDeliverContinuesAfterFailure(t *testing.T) {
	provider := &providerStub{err: errors.New("rate limited")}
	results := Deliver(context.Background(), provider, []Message{{EventID: "e1"}, {EventID: "e2"}})
	if len(results) != 2 || results[0].Status != "failed" || len(provider.sent) != 2 {
		t.Fatalf("results=%+v sent=%d", results, len(provider.sent))
	}
}

func TestConfigAllowsPlaceholdersInMockAndRejectsThemInProduction(t *testing.T) {
	mock := Config{Mode: "local/mock", Enabled: true, DatabaseURL: "CHANGE_ME"}
	if err := mock.Validate(); err != nil {
		t.Fatalf("mock config should validate: %v", err)
	}
	production := mock
	production.Mode = "production"
	if err := production.Validate(); err == nil {
		t.Fatal("production placeholders must be rejected")
	}
}

func TestConfigFromEnvUsesInjectedValues(t *testing.T) {
	values := map[string]string{"DINGTALK_NOTIFY_MODE": "staging", "DINGTALK_NOTIFY_ENABLED": "true", "DATABASE_URL": "postgres://staging"}
	config := ConfigFromEnv(func(key string) string { return values[key] })
	if config.Mode != "staging" || !config.Enabled || config.DatabaseURL != "postgres://staging" {
		t.Fatalf("unexpected config: %+v", config)
	}
}
