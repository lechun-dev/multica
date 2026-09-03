package notify

import (
	"context"
	"testing"
)

func TestAgentChannelServiceScopesOwnerAndAdmin(t *testing.T) {
	store := NewMemoryAgentChannelStore()
	service := AgentChannelService{Store: store}
	owner := ChannelRequester{WorkspaceID: "w1", MemberID: "m1"}
	channel := AgentChannel{WorkspaceID: "w1", AgentID: "a1", ChannelID: "g1", ChannelName: "项目群", RobotCode: "robot", Active: true}
	if err := service.Upsert(context.Background(), owner, channel); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(context.Background(), ChannelRequester{WorkspaceID: "w1", MemberID: "m2"}, "a1"); err == nil {
		t.Fatal("different member should not view owner's channel")
	}
	if _, err := service.List(context.Background(), ChannelRequester{WorkspaceID: "w1", MemberID: "admin", IsAdmin: true}, "a1"); err != nil {
		t.Fatal(err)
	}
}
