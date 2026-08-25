package notify

import (
	"context"
	"errors"
)

// EventPublisher is the only host integration point. The host can adapt its
// comment:created event without importing any Multica business package here.
type EventPublisher interface {
	Publish(ctx context.Context, event MentionCreated) error
}
type EventAdapter struct {
	Enabled   bool
	Publisher EventPublisher
}

func (a EventAdapter) PublishMention(ctx context.Context, event MentionCreated) error {
	if !a.Enabled {
		return ErrDisabled
	}
	if a.Publisher == nil {
		return errors.New("mention event publisher is required")
	}
	if event.EventID == "" || event.WorkspaceID == "" || len(event.Targets) == 0 {
		return errors.New("mention event is incomplete")
	}
	return a.Publisher.Publish(ctx, event)
}
