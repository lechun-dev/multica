package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type AgentChannelStore interface {
	UpsertAgentChannel(ctx context.Context, channel AgentChannel) error
	ListAgentChannels(ctx context.Context, workspaceID, agentID string) ([]AgentChannel, error)
	DeactivateAgentChannel(ctx context.Context, workspaceID, agentID, channelID string, at time.Time) error
}

type ChannelRequester struct {
	WorkspaceID string
	MemberID    string
	IsAdmin     bool
}

type AgentChannelService struct {
	Store AgentChannelStore
	Now   func() time.Time
}

func (s AgentChannelService) Upsert(ctx context.Context, requester ChannelRequester, channel AgentChannel) error {
	if s.Store == nil {
		return errors.New("agent channel store is required")
	}
	if requester.WorkspaceID == "" || channel.WorkspaceID == "" || requester.WorkspaceID != channel.WorkspaceID {
		return errors.New("workspace scope is required")
	}
	if channel.AgentID == "" || channel.ChannelID == "" || channel.ChannelName == "" || channel.RobotCode == "" {
		return errors.New("agent channel, name and robot code are required")
	}
	if channel.OwnerID == "" {
		channel.OwnerID = requester.MemberID
	}
	if !requester.IsAdmin && channel.OwnerID != requester.MemberID {
		return errors.New("only the agent owner or workspace admin may manage channels")
	}
	return s.Store.UpsertAgentChannel(ctx, channel)
}

func (s AgentChannelService) List(ctx context.Context, requester ChannelRequester, agentID string) ([]AgentChannel, error) {
	if s.Store == nil {
		return nil, errors.New("agent channel store is required")
	}
	if requester.WorkspaceID == "" || agentID == "" {
		return nil, errors.New("workspace and agent are required")
	}
	channels, err := s.Store.ListAgentChannels(ctx, requester.WorkspaceID, agentID)
	if err != nil {
		return nil, err
	}
	if requester.IsAdmin {
		return channels, nil
	}
	for _, channel := range channels {
		if channel.OwnerID != "" && channel.OwnerID != requester.MemberID {
			return nil, errors.New("only the agent owner or workspace admin may view channels")
		}
	}
	return channels, nil
}

func (s AgentChannelService) Deactivate(ctx context.Context, requester ChannelRequester, agentID, channelID string) error {
	channels, err := s.List(ctx, requester, agentID)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.ChannelID == channelID {
			now := time.Now()
			if s.Now != nil {
				now = s.Now()
			}
			return s.Store.DeactivateAgentChannel(ctx, requester.WorkspaceID, agentID, channelID, now)
		}
	}
	return fmt.Errorf("agent channel %q not found", channelID)
}

type MemoryAgentChannelStore struct {
	mu       sync.Mutex
	channels map[string]AgentChannel
}

func NewMemoryAgentChannelStore() *MemoryAgentChannelStore {
	return &MemoryAgentChannelStore{channels: make(map[string]AgentChannel)}
}

func agentChannelKey(workspaceID, agentID, channelID string) string {
	return workspaceID + "\x00" + agentID + "\x00" + channelID
}

func (s *MemoryAgentChannelStore) UpsertAgentChannel(_ context.Context, channel AgentChannel) error {
	if channel.WorkspaceID == "" || channel.AgentID == "" || channel.ChannelID == "" {
		return errors.New("agent channel identity is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[agentChannelKey(channel.WorkspaceID, channel.AgentID, channel.ChannelID)] = channel
	return nil
}

func (s *MemoryAgentChannelStore) ListAgentChannels(_ context.Context, workspaceID, agentID string) ([]AgentChannel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]AgentChannel, 0)
	for _, channel := range s.channels {
		if channel.WorkspaceID == workspaceID && channel.AgentID == agentID {
			result = append(result, channel)
		}
	}
	return result, nil
}

func (s *MemoryAgentChannelStore) DeactivateAgentChannel(_ context.Context, workspaceID, agentID, channelID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := agentChannelKey(workspaceID, agentID, channelID)
	channel, ok := s.channels[key]
	if !ok {
		return fmt.Errorf("agent channel %q not found", channelID)
	}
	channel.Active = false
	s.channels[key] = channel
	_ = at
	return nil
}

func (s *MemoryAgentChannelStore) AgentChannels(ctx context.Context, workspaceID, agentID string) ([]AgentChannel, error) {
	return s.ListAgentChannels(ctx, workspaceID, agentID)
}
