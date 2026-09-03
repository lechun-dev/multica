package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Regression tests for the second half of MUL-5492: `issue:updated` used to
// broadcast prev_description alongside the new description, so every debounced
// description autosave pushed TWO full copies of the description to every
// connection in the workspace — including users who did not have the issue open.
//
// The projection has two halves and both must hold: the keys must not reach the
// wire, AND the in-process listeners that genuinely need them must still see
// them. A test that only checks the first half would pass just as happily if the
// producer had stopped populating the keys altogether, which would silently
// break mention notifications and the title-change activity.

// issueUpdatedPayload mirrors the map published by handler.UpdateIssue, trimmed
// to the fields these tests care about.
func issueUpdatedPayload() map[string]any {
	return map[string]any{
		"issue": map[string]any{
			"id":           "issue-1",
			"workspace_id": "ws-1",
			"project_id":   "project-1",
			"revision":     float64(7),
			"title":        "New title",
			"description":  strings.Repeat("new body ", 1024),
		},
		"description_changed": true,
		"title_changed":       true,
		"prev_title":          "Old title",
		"prev_description":    strings.Repeat("old body ", 1024),
		"prev_status":         "todo",
	}
}

func TestIssueUpdatedBroadcast_RedactsBusinessFields(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}

	// A type-specific listener stands in for the real in-process consumers
	// (subscriber_listeners, notification_listeners, activity_listeners). Publish
	// dispatches these before the SubscribeAll forwarder, so this also pins the
	// ordering the projection relies on.
	var seenByListener map[string]any
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		if p, ok := e.Payload.(map[string]any); ok {
			seenByListener = p
		}
	})

	registerListeners(bus, fb)

	bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: "ws-1",
		ActorID:     "member-1",
		ActorType:   "member",
		Payload:     issueUpdatedPayload(),
	})

	if len(fb.workspaceCalls) != 1 {
		t.Fatalf("BroadcastToWorkspace calls = %d, want 1", len(fb.workspaceCalls))
	}
	raw := fb.workspaceCalls[0].msg

	// Half 1: the internal-only keys must not be on the wire at all. Assert on
	// the raw bytes too — a nested copy would still cost the bandwidth this fix
	// is about, even if the top-level key were gone.
	if strings.Contains(string(raw), "prev_description") {
		t.Error("broadcast frame still contains prev_description")
	}
	if strings.Contains(string(raw), "prev_title") {
		t.Error("broadcast frame still contains prev_title")
	}

	var frame struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if frame.Type != protocol.EventIssueUpdated {
		t.Errorf("frame type = %q, want %q", frame.Type, protocol.EventIssueUpdated)
	}
	if _, present := frame.Payload["prev_description"]; present {
		t.Error("payload still carries prev_description")
	}
	if _, present := frame.Payload["prev_title"]; present {
		t.Error("payload still carries prev_title")
	}

	// Only routing/version fields cross the workspace boundary. Clients use
	// these to invalidate and then refetch through the permission-aware API.
	issue, ok := frame.Payload["issue"].(map[string]any)
	if !ok {
		t.Fatal("payload lost the issue object")
	}
	for _, key := range []string{"title", "description"} {
		if _, present := issue[key]; present {
			t.Errorf("issue.%s reached workspace broadcast", key)
		}
	}
	if issue["id"] != "issue-1" || issue["project_id"] != "project-1" {
		t.Errorf("routing fields were not preserved: %#v", issue)
	}
	if frame.Payload["description_changed"] != true {
		t.Error("description_changed flag was lost")
	}
	if frame.Payload["title_changed"] != true {
		t.Error("title_changed flag was lost")
	}
	if _, present := frame.Payload["prev_status"]; present {
		t.Error("private previous-value field reached workspace broadcast")
	}

	// Half 2: the in-process listener still received the full payload.
	if seenByListener == nil {
		t.Fatal("in-process listener did not run")
	}
	if _, present := seenByListener["prev_description"]; !present {
		t.Error("in-process listener lost prev_description; mention diffing would break")
	}
	if _, present := seenByListener["prev_title"]; !present {
		t.Error("in-process listener lost prev_title; the title-change activity would break")
	}
}

// TestProjectOutbound_DoesNotMutateProducerPayload guards the copy-on-project
// behaviour. Mutating the producer's map in place would corrupt it for any
// listener or forwarder that reads it afterwards.
func TestProjectOutbound_DoesNotMutateProducerPayload(t *testing.T) {
	original := issueUpdatedPayload()

	projected := projectOutbound(protocol.EventIssueUpdated, original)

	if _, present := original["prev_description"]; !present {
		t.Error("projectOutbound mutated the producer's payload map")
	}
	pm, ok := projected.(map[string]any)
	if !ok {
		t.Fatalf("projected payload type = %T, want map[string]any", projected)
	}
	if _, present := pm["prev_description"]; present {
		t.Error("projected payload still has prev_description")
	}
	if _, present := pm["issue"]; !present {
		t.Error("projected payload lost routing issue object")
	}
}

func TestProjectOutbound_TaskMessageRedactsContent(t *testing.T) {
	original := map[string]any{
		"task_id": "task-1", "issue_id": "issue-1", "seq": 3,
		"type": "tool_result", "content": "private transcript",
		"input": map[string]any{"secret": "value"}, "output": "private output",
	}
	projected, ok := projectOutbound(protocol.EventTaskMessage, original).(map[string]any)
	if !ok {
		t.Fatal("task:message projection did not return a map")
	}
	if projected["task_id"] != "task-1" || projected["issue_id"] != "issue-1" || projected["type"] != "tool_result" {
		t.Fatalf("routing/type metadata was lost: %#v", projected)
	}
	for _, key := range []string{"content", "input", "output"} {
		if _, present := projected[key]; present {
			t.Errorf("private task message field %q reached workspace broadcast", key)
		}
	}
	if original["content"] != "private transcript" {
		t.Error("task message projection mutated the producer payload")
	}
}

func TestProjectOutbound_ChatDoneRedactsContent(t *testing.T) {
	original := map[string]any{
		"chat_session_id": "chat-1", "task_id": "task-1", "message_id": "msg-1",
		"content": "private answer", "message_kind": "message",
	}
	projected, ok := projectOutbound(protocol.EventChatDone, original).(map[string]any)
	if !ok {
		t.Fatal("chat:done projection did not return a map")
	}
	if projected["chat_session_id"] != "chat-1" || projected["message_id"] != "msg-1" {
		t.Fatalf("chat routing metadata was lost: %#v", projected)
	}
	if _, present := projected["content"]; present {
		t.Fatal("chat content reached workspace broadcast")
	}
}

func TestProjectOutbound_IssueUpdatedProjectsTypedPayload(t *testing.T) {
	type typedIssue struct {
		ID          string `json:"id"`
		ProjectID   string `json:"project_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	type typedPayload struct {
		Issue              typedIssue `json:"issue"`
		DescriptionChanged bool       `json:"description_changed"`
	}

	projected, ok := projectOutbound(protocol.EventIssueUpdated, typedPayload{
		Issue: typedIssue{
			ID: "issue-1", ProjectID: "project-1", Title: "private", Description: "private",
		},
		DescriptionChanged: true,
	}).(map[string]any)
	if !ok {
		t.Fatal("typed issue payload was not projected to a map")
	}
	issue, ok := projected["issue"].(map[string]any)
	if !ok {
		t.Fatalf("typed issue projection missing issue map: %#v", projected)
	}
	if issue["id"] != "issue-1" || issue["project_id"] != "project-1" {
		t.Fatalf("typed routing fields were lost: %#v", issue)
	}
	if _, present := issue["title"]; present {
		t.Fatal("typed title reached workspace broadcast")
	}
}

func TestProjectOutbound_TaskFailedKeepsErrorInternal(t *testing.T) {
	original := map[string]any{
		"task_id":        "task-1",
		"failure_reason": "timeout",
		"retry_pending":  false,
		"error":          "task timed out",
	}

	projected, ok := projectOutbound(protocol.EventTaskFailed, original).(map[string]any)
	if !ok {
		t.Fatal("task:failed projection did not return a map")
	}
	if _, present := projected["error"]; present {
		t.Fatal("task:failed error reached the workspace realtime payload")
	}
	if projected["failure_reason"] != "timeout" || projected["retry_pending"] != false {
		t.Fatalf("safe task failure metadata was lost: %#v", projected)
	}
	if original["error"] != "task timed out" {
		t.Fatal("task:failed projection mutated the in-process payload")
	}
}

func TestTaskFailedBroadcast_DeliversErrorOnlyInProcess(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	payload := map[string]any{
		"task_id":        "task-1",
		"failure_reason": "timeout",
		"retry_pending":  false,
		"error":          "task timed out",
	}

	var inProcessError string
	bus.Subscribe(protocol.EventTaskFailed, func(e events.Event) {
		m, _ := e.Payload.(map[string]any)
		inProcessError, _ = m["error"].(string)
	})
	registerListeners(bus, fb)
	bus.Publish(events.Event{
		Type:        protocol.EventTaskFailed,
		WorkspaceID: "workspace-1",
		Payload:     payload,
	})

	if inProcessError != "task timed out" {
		t.Fatalf("in-process channel listener error = %q, want task timed out", inProcessError)
	}
	if len(fb.workspaceCalls) != 1 {
		t.Fatalf("workspace broadcasts = %d, want 1", len(fb.workspaceCalls))
	}
	var frame struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(fb.workspaceCalls[0].msg, &frame); err != nil {
		t.Fatalf("unmarshal workspace frame: %v", err)
	}
	if _, present := frame.Payload["error"]; present {
		t.Fatal("channel-only failure error reached the workspace broadcast")
	}
	if payload["error"] != "task timed out" {
		t.Fatal("broadcast projection mutated the producer payload")
	}
}

// 2026-08-27 coder(lq): Autopilot events are workspace-wide cache invalidation
// signals, not a data transport. Pin that private titles, assignees, IDs, and
// run details never cross the WebSocket boundary while in-process consumers
// still receive the original event payload.
func TestAutopilotBroadcast_StripsPrivatePayload(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	payload := map[string]any{
		"autopilot": map[string]any{
			"id":          "autopilot-private",
			"title":       "private automation",
			"assignee_id": "agent-private",
		},
	}

	var inProcessPayload map[string]any
	bus.Subscribe(protocol.EventAutopilotUpdated, func(e events.Event) {
		inProcessPayload, _ = e.Payload.(map[string]any)
	})
	registerListeners(bus, fb)
	bus.Publish(events.Event{
		Type:        protocol.EventAutopilotUpdated,
		WorkspaceID: "workspace-1",
		Payload:     payload,
	})

	if len(fb.workspaceCalls) != 1 {
		t.Fatalf("workspace broadcasts = %d, want 1", len(fb.workspaceCalls))
	}
	var frame struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(fb.workspaceCalls[0].msg, &frame); err != nil {
		t.Fatalf("unmarshal workspace frame: %v", err)
	}
	if len(frame.Payload) != 0 {
		t.Fatalf("private autopilot payload reached workspace broadcast: %#v", frame.Payload)
	}
	if inProcessPayload["autopilot"] == nil {
		t.Fatal("in-process listener lost the original autopilot payload")
	}
	if payload["autopilot"] == nil {
		t.Fatal("broadcast projection mutated the producer payload")
	}
}

// TestProjectOutbound_PassesThroughUnlistedEvents keeps the projection from
// becoming a general-purpose payload filter: an event type with no entry in the
// table must be forwarded byte-for-byte, and a non-map payload must survive.
func TestProjectOutbound_PassesThroughUnlistedEvents(t *testing.T) {
	payload := map[string]any{"prev_description": "kept"}
	if got := projectOutbound(protocol.EventTaskProgress, payload); got == nil {
		t.Fatal("unlisted event type returned nil payload")
	} else if m, ok := got.(map[string]any); !ok || m["prev_description"] != "kept" {
		t.Error("unlisted event type must pass through untouched")
	}

	// Typed (non-map) payloads are common elsewhere in the bus.
	type typedPayload struct{ ID string }
	tp := typedPayload{ID: "x"}
	if got := projectOutbound(protocol.EventTaskProgress, tp); got != any(tp) {
		t.Errorf("unlisted event type was altered: got %#v, want %#v", got, tp)
	}
}

func TestProjectOutbound_DeletionEventsKeepOnlyRoutingMetadata(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload map[string]any
		want    map[string]any
		redact  []string
	}{
		{
			name:    "issue deleted",
			event:   protocol.EventIssueDeleted,
			payload: map[string]any{"issue_id": "issue-1", "title": "private title", "description": "private body"},
			want:    map[string]any{"issue_id": "issue-1"},
			redact:  []string{"title", "description"},
		},
		{
			name:    "comment deleted",
			event:   protocol.EventCommentDeleted,
			payload: map[string]any{"comment_id": "comment-1", "issue_id": "issue-1", "issue_revision": int64(9), "content": "private comment"},
			want:    map[string]any{"comment_id": "comment-1", "issue_id": "issue-1", "issue_revision": int64(9)},
			redact:  []string{"content"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projected, ok := projectOutbound(tt.event, tt.payload).(map[string]any)
			if !ok {
				t.Fatalf("projection type = %T, want map[string]any", projectOutbound(tt.event, tt.payload))
			}
			for key, want := range tt.want {
				if got := projected[key]; got != want {
					t.Errorf("projected[%q] = %#v, want %#v", key, got, want)
				}
			}
			for _, key := range tt.redact {
				if _, present := projected[key]; present {
					t.Errorf("private field %q reached deletion event projection", key)
				}
			}
			if len(projected) != len(tt.want) {
				t.Errorf("projected keys = %#v, want exactly %#v", projected, tt.want)
			}
		})
	}
}

func TestProjectOutbound_ProjectResourceRedactsResourceDetails(t *testing.T) {
	original := map[string]any{
		"project_id": "project-1",
		"resource": map[string]any{
			"id": "resource-1", "project_id": "project-1",
			"resource_ref": map[string]any{"url": "https://github.com/private/repo", "daemon_id": "daemon-1"},
			"label":        "private checkout",
		},
	}
	projected, ok := projectOutbound(protocol.EventProjectResourceCreated, original).(map[string]any)
	if !ok {
		t.Fatalf("project resource projection type = %T, want map[string]any", projectOutbound(protocol.EventProjectResourceCreated, original))
	}
	if projected["project_id"] != "project-1" || projected["resource_id"] != "resource-1" {
		t.Fatalf("resource routing metadata was lost: %#v", projected)
	}
	for _, key := range []string{"resource", "resource_ref", "label", "daemon_id"} {
		if _, present := projected[key]; present {
			t.Errorf("private project resource field %q reached workspace broadcast", key)
		}
	}
	if original["resource"] == nil {
		t.Fatal("project resource projection mutated the producer payload")
	}
}

func TestProjectOutbound_ProjectResourceDeletedKeepsIDs(t *testing.T) {
	projected, ok := projectOutbound(protocol.EventProjectResourceDeleted, map[string]any{
		"project_id": "project-1", "resource_id": "resource-1", "resource_ref": "/private/path",
	}).(map[string]any)
	if !ok {
		t.Fatalf("project resource deletion projection type = %T, want map[string]any", projectOutbound(protocol.EventProjectResourceDeleted, nil))
	}
	if len(projected) != 2 || projected["project_id"] != "project-1" || projected["resource_id"] != "resource-1" {
		t.Fatalf("unexpected project resource deletion projection: %#v", projected)
	}
}
