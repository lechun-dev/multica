package notify

import (
	"context"
	"testing"
)

type publisherStub struct{ events []MentionCreated }

func (p *publisherStub) Publish(_ context.Context, event MentionCreated) error {
	p.events = append(p.events, event)
	return nil
}

func TestEventAdapterIsFeatureFlagged(t *testing.T) {
	publisher := &publisherStub{}
	event := MentionCreated{EventID: "e1", WorkspaceID: "w1", Targets: []MentionTarget{{ID: "m1", Kind: "member"}}}
	if err := (EventAdapter{Publisher: publisher}).PublishMention(context.Background(), event); err != ErrDisabled {
		t.Fatalf("disabled adapter should be a no-op: %v", err)
	}
	if err := (EventAdapter{Enabled: true, Publisher: publisher}).PublishMention(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 1 || publisher.events[0].EventID != "e1" {
		t.Fatalf("event was not published: %+v", publisher.events)
	}
}

func TestAdaptCommentMentionDeduplicatesTargets(t *testing.T) {
	event, err := AdaptCommentMention(CommentMention{EventID: "e1", WorkspaceID: "w1", Targets: []MentionTarget{{ID: "m1", Kind: "member"}, {ID: "m1", Kind: "member"}}})
	if err != nil || len(event.Targets) != 1 || event.CreatedAt.IsZero() {
		t.Fatalf("event=%+v err=%v", event, err)
	}
}
