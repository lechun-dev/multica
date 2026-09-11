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
			"revision":     2,
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

func TestIssueUpdatedBroadcast_UsesSafeProjectionWithoutMutatingInternalPayload(t *testing.T) {
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

	// 2026-09-11 coder(lq): Workspace WebSocket fanout is only an invalidation
	// signal. Business content must be fetched through the permission-aware API.
	for _, privateKey := range []string{"prev_description", "prev_title", "prev_status", "description", "title"} {
		if strings.Contains(string(raw), privateKey) {
			t.Errorf("broadcast frame still contains private key %q", privateKey)
		}
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
	for _, privateKey := range []string{"prev_description", "prev_title", "prev_status"} {
		if _, present := frame.Payload[privateKey]; present {
			t.Errorf("payload still carries %s", privateKey)
		}
	}

	// Routing fields and change flags survive so clients can invalidate and
	// refetch the authoritative record without receiving its private content.
	issue, ok := frame.Payload["issue"].(map[string]any)
	if !ok {
		t.Fatal("payload lost the issue object")
	}
	if issue["id"] != "issue-1" || issue["workspace_id"] != "ws-1" || issue["project_id"] != "project-1" {
		t.Fatalf("issue routing projection is incomplete: %#v", issue)
	}
	if issue["revision"] != float64(2) {
		t.Errorf("issue revision = %#v, want 2", issue["revision"])
	}
	if _, present := issue["description"]; present {
		t.Error("issue.description reached the workspace broadcast")
	}
	if _, present := issue["title"]; present {
		t.Error("issue.title reached the workspace broadcast")
	}
	if frame.Payload["issue_id"] != "issue-1" {
		t.Errorf("issue_id = %#v, want issue-1", frame.Payload["issue_id"])
	}
	if frame.Payload["description_changed"] != true {
		t.Error("description_changed flag was lost")
	}
	if frame.Payload["title_changed"] != true {
		t.Error("title_changed flag was lost")
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
	if seenByListener["prev_status"] != "todo" {
		t.Error("in-process listener lost prev_status")
	}
	internalIssue, ok := seenByListener["issue"].(map[string]any)
	if !ok || internalIssue["description"] == nil || internalIssue["title"] != "New title" {
		t.Error("in-process listener lost full issue content")
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
	if _, present := pm["prev_status"]; present {
		t.Error("projected payload still has prev_status")
	}
	projectedIssue, ok := pm["issue"].(map[string]any)
	if !ok {
		t.Fatal("projected payload lost issue routing fields")
	}
	if _, present := projectedIssue["description"]; present {
		t.Error("projected issue still has description")
	}
	originalIssue, ok := original["issue"].(map[string]any)
	if !ok || originalIssue["description"] == nil {
		t.Error("projectOutbound mutated the producer's nested issue payload")
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
	const unlistedEvent = "test:unlisted"
	payload := map[string]any{"prev_description": "kept"}
	if got := projectOutbound(unlistedEvent, payload); got == nil {
		t.Fatal("unlisted event type returned nil payload")
	} else if m, ok := got.(map[string]any); !ok || m["prev_description"] != "kept" {
		t.Error("unlisted event type must pass through untouched")
	}

	// Typed (non-map) payloads are common elsewhere in the bus.
	type typedPayload struct{ ID string }
	tp := typedPayload{ID: "x"}
	if got := projectOutbound(unlistedEvent, tp); got != any(tp) {
		t.Errorf("non-map payload was altered: got %#v, want %#v", got, tp)
	}
}
