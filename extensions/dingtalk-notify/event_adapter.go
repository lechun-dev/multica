package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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

// CommentMention is the deliberately small shape a host comment listener
// needs to provide. Keeping this type in the extension prevents the host
// adapter from importing notification internals or leaking database models.
type CommentMention struct {
	EventID         string
	WorkspaceID     string
	Actor           Actor
	Targets         []MentionTarget
	Body            string
	SourceURL       string
	WorkspaceName   string
	ProjectName     string
	IssueIdentifier string
	IssueTitle      string
	CreatedAt       time.Time
}

func AdaptCommentMention(input CommentMention) (MentionCreated, error) {
	if strings.TrimSpace(input.EventID) == "" || strings.TrimSpace(input.WorkspaceID) == "" {
		return MentionCreated{}, errors.New("comment mention event_id and workspace_id are required")
	}
	if len(input.Targets) == 0 {
		return MentionCreated{}, errors.New("comment mention must contain at least one target")
	}
	seen := make(map[string]struct{}, len(input.Targets))
	targets := make([]MentionTarget, 0, len(input.Targets))
	for _, target := range input.Targets {
		if strings.TrimSpace(target.ID) == "" {
			return MentionCreated{}, errors.New("comment mention target id is required")
		}
		if target.Kind != "member" && target.Kind != "agent" {
			return MentionCreated{}, fmt.Errorf("unsupported mention target kind %q", target.Kind)
		}
		key := target.Kind + "\x00" + target.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return MentionCreated{
		EventID: input.EventID, WorkspaceID: input.WorkspaceID, Actor: input.Actor,
		Targets: targets, Text: input.Body, SourceURL: input.SourceURL,
		WorkspaceName: input.WorkspaceName, ProjectName: input.ProjectName,
		IssueIdentifier: input.IssueIdentifier, IssueTitle: input.IssueTitle,
		CreatedAt: createdAt,
	}, nil
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
