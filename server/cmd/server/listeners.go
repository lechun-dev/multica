package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// internalOnlyPayloadKeys lists payload keys that exist purely for in-process
// listeners and must never be serialized to a WebSocket client.
//
// `issue:updated` carries prev_description and prev_title so the in-process
// listeners can diff against the new values: subscriber_listeners.go adds newly
// @mentioned users, notification_listeners.go builds mention notifications, and
// activity_listeners.go records the title change. Those all run on
// bus.Subscribe, which Publish dispatches BEFORE the SubscribeAll forwarder
// below, so removing the keys on the way out cannot affect them.
//
// No client reads either key — IssueUpdatedPayload in
// packages/core/types/events.ts does not declare them. They reached the wire
// only because the forwarder reuses the producer's payload map verbatim, which
// meant every description autosave broadcast TWO full copies of the description
// (the new one inside `issue`, plus prev_description) to every connection in the
// workspace, including users who did not have the issue open. The DB write is
// O(1); the fanout was O(workspace connections × description size) (MUL-5492).
//
// This is a table rather than an `if` on one event type because the bug was
// structural, not a typo: the next large field added to a published payload
// inherits the same cost silently. Keeping the list declarative puts the
// internal/external payload boundary in one reviewable place.
var internalOnlyPayloadKeys = map[string][]string{
	protocol.EventIssueUpdated: {"prev_description", "prev_title"},
	// task:failed error text is consumed synchronously by channel outbounds.
	// It may contain provider/runtime detail that belongs in the originating
	// chat transcript, not in the workspace-wide realtime fanout.
	protocol.EventTaskFailed: {"error"},
}

// 2026-09-03 coder(lq): Workspace fanout is not a permission boundary. These
// events therefore carry only routing/state hints; the client refetches the
// authoritative, permission-filtered resource before rendering business data.
var workspaceSafeProjectionEvents = map[string]bool{
	protocol.EventIssueCreated:              true,
	protocol.EventIssueUpdated:              true,
	protocol.EventProjectCreated:            true,
	protocol.EventProjectUpdated:            true,
	protocol.EventProjectDeleted:            true,
	protocol.EventProjectResourceCreated:    true,
	protocol.EventProjectResourceUpdated:    true,
	protocol.EventProjectResourceDeleted:    true,
	protocol.EventIssueDeleted:              true,
	protocol.EventIssueAttachmentsChanged:   true,
	protocol.EventIssueLabelsChanged:        true,
	protocol.EventIssueMetadataChanged:      true,
	protocol.EventIssuePropertiesChanged:    true,
	protocol.EventCommentCreated:            true,
	protocol.EventCommentUpdated:            true,
	protocol.EventCommentDeleted:            true,
	protocol.EventCommentResolved:           true,
	protocol.EventCommentUnresolved:         true,
	protocol.EventActivityCreated:           true,
	protocol.EventReactionAdded:             true,
	protocol.EventReactionRemoved:           true,
	protocol.EventIssueReactionAdded:        true,
	protocol.EventIssueReactionRemoved:      true,
	protocol.EventSubscriberAdded:           true,
	protocol.EventSubscriberRemoved:         true,
	protocol.EventTaskQueued:                true,
	protocol.EventTaskDispatch:              true,
	protocol.EventTaskRunning:               true,
	protocol.EventTaskWaitingLocalDirectory: true,
	protocol.EventTaskCompleted:             true,
	protocol.EventTaskFailed:                true,
	protocol.EventTaskCancelled:             true,
	protocol.EventTaskMessage:               true,
	protocol.EventChatMessage:               true,
	protocol.EventChatDone:                  true,
	protocol.EventChatCancelFinalized:       true,
}

func payloadAsMap(payload any) (map[string]any, bool) {
	if m, ok := payload.(map[string]any); ok {
		copy := make(map[string]any, len(m))
		for k, v := range m {
			copy[k] = v
		}
		return copy, true
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}

func safeIssueProjection(payload map[string]any, updated bool) map[string]any {
	projected := make(map[string]any)
	if issueRaw, present := payload["issue"]; present {
		issue, ok := payloadAsMap(issueRaw)
		if !ok {
			issue = nil
		}
		if issue != nil {
			safeIssue := make(map[string]any)
			for _, key := range []string{"id", "workspace_id", "project_id", "parent_issue_id", "revision"} {
				if value, present := issue[key]; present {
					safeIssue[key] = value
				}
			}
			if len(safeIssue) > 0 {
				projected["issue"] = safeIssue
				if id, ok := issue["id"].(string); ok {
					projected["issue_id"] = id
				}
			}
		}
	}
	if issueID, ok := payload["issue_id"].(string); ok && issueID != "" {
		projected["issue_id"] = issueID
	}
	if updated {
		for _, key := range []string{"revision", "assignee_changed", "status_changed", "priority_changed", "project_changed", "start_date_changed", "due_date_changed", "description_changed", "title_changed"} {
			if value, present := payload[key]; present {
				projected[key] = value
			}
		}
	}
	return projected
}

func safeTaskOrChatProjection(eventType string, payload map[string]any) map[string]any {
	projected := make(map[string]any)
	for _, key := range []string{"task_id", "issue_id", "chat_session_id", "seq", "type", "status", "outcome", "step", "total", "failure_reason", "retry_pending", "message_id", "role", "created_at", "message_kind", "quick_actions_pending", "initiator_user_id"} {
		if value, present := payload[key]; present {
			projected[key] = value
		}
	}
	return projected
}

// 2026-09-03 coder(lq): Issue and timeline auxiliary events are workspace
// broadcasts, not per-issue permission channels. Keep only identifiers and
// revisions so clients can invalidate and refetch through the permission-aware
// HTTP API without disclosing comment text, activity details, labels, or
// custom-field values to unrelated project members.
func safeIssueRelatedProjection(eventType string, payload map[string]any) map[string]any {
	projected := make(map[string]any)
	for _, key := range []string{
		"issue_id", "issue_revision", "comment_id", "comment_revision",
		"label_id", "property_id", "user_type", "user_id", "reason", "emoji",
		"actor_type", "actor_id",
	} {
		if value, present := payload[key]; present {
			projected[key] = value
		}
	}

	if eventType == protocol.EventCommentCreated || eventType == protocol.EventCommentUpdated ||
		eventType == protocol.EventCommentDeleted ||
		eventType == protocol.EventCommentResolved || eventType == protocol.EventCommentUnresolved {
		if commentRaw, present := payload["comment"]; present {
			if comment, ok := payloadAsMap(commentRaw); ok {
				safeComment := make(map[string]any)
				for _, key := range []string{"id", "issue_id", "parent_id", "revision", "resolved_at", "resolved_by_type", "resolved_by_id"} {
					if value, present := comment[key]; present {
						safeComment[key] = value
					}
				}
				if len(safeComment) > 0 {
					projected["comment"] = safeComment
					if _, present := projected["comment_id"]; !present {
						if id, ok := comment["id"].(string); ok {
							projected["comment_id"] = id
						}
					}
					if _, present := projected["issue_id"]; !present {
						if id, ok := comment["issue_id"].(string); ok {
							projected["issue_id"] = id
						}
					}
				}
			}
		}
	} else if eventType == protocol.EventActivityCreated {
		if entryRaw, present := payload["entry"]; present {
			if entry, ok := payloadAsMap(entryRaw); ok {
				if id, ok := entry["id"].(string); ok {
					projected["entry"] = map[string]any{"id": id}
				}
			}
		}
	}
	return projected
}

// 2026-09-03 coder(lq): Project resources can contain repository URLs, local
// paths, daemon IDs, and labels. Workspace fanout only needs the project and
// resource IDs; the client refetches the resource through the permission-aware
// project endpoint when it needs the full record.
func safeProjectResourceProjection(payload map[string]any) map[string]any {
	projected := make(map[string]any)
	for _, key := range []string{"project_id", "resource_id"} {
		if value, present := payload[key]; present {
			projected[key] = value
		}
	}
	if resourceRaw, present := payload["resource"]; present {
		if resource, ok := payloadAsMap(resourceRaw); ok {
			if _, present := projected["project_id"]; !present {
				if value, ok := resource["project_id"].(string); ok && value != "" {
					projected["project_id"] = value
				}
			}
			if _, present := projected["resource_id"]; !present {
				if value, ok := resource["id"].(string); ok && value != "" {
					projected["resource_id"] = value
				}
			}
		}
	}
	return projected
}

func safeProjectProjection(payload map[string]any) map[string]any {
	projected := make(map[string]any)
	if projectRaw, present := payload["project"]; present {
		if project, ok := payloadAsMap(projectRaw); ok {
			for _, key := range []string{"id", "workspace_id"} {
				if value, present := project[key]; present {
					projected[key] = value
				}
			}
		}
	}
	if projectID, ok := payload["project_id"].(string); ok && projectID != "" {
		projected["project_id"] = projectID
	}
	return projected
}

// projectOutbound returns payload with the event type's internal-only keys
// removed, ready to serialize for external consumers.
//
// The input map is never mutated. In-process listeners have already run by the
// time this is called, but the producer still owns the map and a second
// forwarder may yet read it, so mutating it in place would be a landmine.
func projectOutbound(eventType string, payload any) any {
	// 2026-08-27 coder(lq): Autopilot visibility is narrower than workspace
	// membership, while the realtime hub still fans these events out by
	// workspace. Clients only use this event family to invalidate and refetch
	// permission-filtered API queries, so expose an empty refresh signal and
	// keep titles, assignees, resource IDs, and run details off the wire.
	if strings.HasPrefix(eventType, "autopilot:") {
		return map[string]any{}
	}
	if workspaceSafeProjectionEvents[eventType] {
		m, ok := payloadAsMap(payload)
		if !ok {
			return map[string]any{}
		}
		switch eventType {
		case protocol.EventIssueCreated:
			return safeIssueProjection(m, false)
		case protocol.EventIssueUpdated:
			return safeIssueProjection(m, true)
		case protocol.EventProjectCreated, protocol.EventProjectUpdated, protocol.EventProjectDeleted:
			return safeProjectProjection(m)
		case protocol.EventProjectResourceCreated, protocol.EventProjectResourceUpdated, protocol.EventProjectResourceDeleted:
			return safeProjectResourceProjection(m)
		case protocol.EventIssueDeleted:
			return safeIssueRelatedProjection(eventType, m)
		case protocol.EventIssueLabelsChanged, protocol.EventIssueAttachmentsChanged,
			protocol.EventIssueMetadataChanged, protocol.EventIssuePropertiesChanged,
			protocol.EventCommentCreated, protocol.EventCommentUpdated,
			protocol.EventCommentDeleted, protocol.EventCommentResolved, protocol.EventCommentUnresolved,
			protocol.EventActivityCreated, protocol.EventReactionAdded,
			protocol.EventReactionRemoved, protocol.EventIssueReactionAdded,
			protocol.EventIssueReactionRemoved, protocol.EventSubscriberAdded,
			protocol.EventSubscriberRemoved:
			return safeIssueRelatedProjection(eventType, m)
		default:
			return safeTaskOrChatProjection(eventType, m)
		}
	}
	keys := internalOnlyPayloadKeys[eventType]
	if len(keys) == 0 {
		return payload
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	projected := make(map[string]any, len(m))
	for k, v := range m {
		projected[k] = v
	}
	for _, k := range keys {
		delete(projected, k)
	}
	return projected
}

// registerListeners wires up event bus listeners for WS broadcasting.
// Personal events (inbox, invites) are sent only to the target user via
// SendToUser. All other events are broadcast to the workspace room.
//
// The broadcaster parameter is intentionally typed as the realtime.Broadcaster
// interface (not *realtime.Hub) so that this layer can later be swapped out
// for a Redis-backed relay or a feature-flagged dual-write implementation
// without touching any of the event listeners below. This is Phase 0 of the
// horizontal-scaling plan tracked in MUL-1138.
func registerListeners(bus *events.Bus, b realtime.Broadcaster) {
	// Personal events should NOT be broadcast to the whole workspace.
	personalEvents := map[string]bool{
		protocol.EventInboxNew:           true,
		protocol.EventInboxRead:          true,
		protocol.EventInboxArchived:      true,
		protocol.EventInboxUnarchived:    true,
		protocol.EventInboxBatchRead:     true,
		protocol.EventInboxBatchArchived: true,
		protocol.EventInvitationCreated:  true,
		protocol.EventInvitationRevoked:  true,
		protocol.EventChatSessionCreated: true,
		protocol.EventChatSessionUpdated: true,
	}

	// Helper: marshal event and send to a specific user.
	sendToRecipient := func(b realtime.Broadcaster, e events.Event, recipientID string) {
		if recipientID == "" {
			return
		}
		data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
		if err != nil {
			return
		}
		realtime.M.RecordEvent(e.Type)
		b.SendToUser(recipientID, data)
	}

	// inbox:new — extract recipient from nested item
	bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		item, ok := payload["item"].(map[string]any)
		if !ok {
			return
		}
		recipientID, _ := item["recipient_id"].(string)
		sendToRecipient(b, e, recipientID)
	})

	// inbox:read, inbox:archived, inbox:unarchived, inbox:batch-read,
	// inbox:batch-archived — extract recipient from top-level payload
	for _, eventType := range []string{
		protocol.EventInboxRead, protocol.EventInboxArchived, protocol.EventInboxUnarchived,
		protocol.EventInboxBatchRead, protocol.EventInboxBatchArchived,
	} {
		bus.Subscribe(eventType, func(e events.Event) {
			payload, ok := e.Payload.(map[string]any)
			if !ok {
				return
			}
			recipientID, _ := payload["recipient_id"].(string)
			sendToRecipient(b, e, recipientID)
		})
	}

	// invitation:created — send to the invitee so they see the invitation in real time.
	bus.Subscribe(protocol.EventInvitationCreated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		inv, ok := payload["invitation"].(handler.InvitationResponse)
		if !ok {
			// Fallback for map encoding.
			if invMap, ok := payload["invitation"].(map[string]any); ok {
				if uid, _ := invMap["invitee_user_id"].(*string); uid != nil && *uid != "" {
					data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
					if err != nil {
						return
					}
					realtime.M.RecordEvent(e.Type)
					b.SendToUser(*uid, data)
				}
			}
			return
		}
		if inv.InviteeUserID != nil && *inv.InviteeUserID != "" {
			data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
			if err != nil {
				return
			}
			realtime.M.RecordEvent(e.Type)
			b.SendToUser(*inv.InviteeUserID, data)
		}
	})

	// invitation:revoked — send to the invitee so their pending list updates.
	bus.Subscribe(protocol.EventInvitationRevoked, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		uid, _ := payload["invitee_user_id"].(*string)
		if uid != nil && *uid != "" {
			sendToRecipient(b, e, *uid)
		}
	})

	// A Chat session is creator-private. Its initial title may be derived from
	// the creator's first message, so the list-invalidation event must not be
	// broadcast to every workspace member. ActorID is the creator on every
	// producer path for this event.
	for _, eventType := range []string{protocol.EventChatSessionCreated, protocol.EventChatSessionUpdated} {
		bus.Subscribe(eventType, func(e events.Event) {
			sendToRecipient(b, e, e.ActorID)
		})
	}

	// member:added — also send to the invited user so they discover the new workspace.
	// Pass excludeWorkspace so clients already in the target room (reached via
	// BroadcastToWorkspace in SubscribeAll) don't receive the event twice.
	bus.Subscribe(protocol.EventMemberAdded, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		var userID string
		switch m := payload["member"].(type) {
		case handler.MemberWithUserResponse:
			userID = m.UserID
		case map[string]any:
			userID, _ = m["user_id"].(string)
		default:
			slog.Warn("member:added: unexpected member payload type", "type", fmt.Sprintf("%T", payload["member"]))
		}
		if userID == "" {
			return
		}
		data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
		if err != nil {
			return
		}
		realtime.M.RecordEvent(e.Type)
		b.SendToUser(userID, data, e.WorkspaceID)
	})

	// SubscribeAll handles workspace-broadcast for non-personal events.
	bus.SubscribeAll(func(e events.Event) {
		// Skip personal events — they are handled by type-specific listeners above.
		if personalEvents[e.Type] {
			return
		}

		msg := map[string]any{
			"type":       e.Type,
			"payload":    projectOutbound(e.Type, e.Payload),
			"actor_id":   e.ActorID,
			"actor_type": e.ActorType,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			slog.Error("failed to marshal event", "event_type", e.Type, "error", err)
			return
		}

		// Phase 1 (MUL-1138): the per-resource scope routing for high-frequency
		// task/chat events is intentionally NOT enabled yet. The server-side
		// pieces — Hub.subscribe/unsubscribe protocol, ScopeAuthorizer, Redis
		// Streams relay — have all landed, but the client (WSClient + the
		// per-page chat/task hooks) does not yet send `subscribe` frames or
		// replay subscriptions on reconnect. Routing these events through
		// `BroadcastToScope("task"|"chat", ...)` today would silently drop
		// every chat/task message on the floor, breaking the live chat
		// timeline, chat unread badges, and pending-task UI.
		//
		// Until the client lands its scope-subscription PR, we keep
		// task/chat events on workspace fanout (same behavior as before this
		// PR). The `Event.TaskID` / `Event.ChatSessionID` hints are still
		// populated by producers so that flipping the switch later is a
		// one-line change here. See review on PR #1429 for context.

		if e.WorkspaceID != "" {
			realtime.M.RecordEvent(e.Type)
			b.BroadcastToWorkspace(e.WorkspaceID, data)
		} else if strings.HasPrefix(e.Type, "daemon:") {
			realtime.M.RecordEvent(e.Type)
			b.Broadcast(data)
		}
		// Otherwise drop — no global broadcast for non-daemon events without a workspace.
	})
}
