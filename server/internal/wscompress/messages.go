package wscompress

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func strictJSON(data []byte, dst any) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}

// safeMessage is an explicit schema allowlist, not a substring scan of a
// message prefix. Unknown message types and unknown fields stay uncompressed.
// Authentication, all RPC responses (including tasks.claim credentials), tool
// input/output, attachments, invitations and provider config are excluded.
func safeMessage(endpoint Endpoint, data []byte) bool {
	var msg struct {
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
		ActorID   string          `json:"actor_id"`
		ActorType string          `json:"actor_type"`
		EventID   string          `json:"event_id"`
	}
	if !strictJSON(data, &msg) || !cleanText(msg.ActorID, msg.ActorType, msg.EventID) {
		return false
	}
	if endpoint == Daemon {
		switch msg.Type {
		case protocol.EventDaemonHeartbeatAck:
			var p protocol.DaemonHeartbeatAckPayload
			return strictJSON(msg.Payload, &p) && cleanJSONStrings(msg.Payload)
		case protocol.EventDaemonTaskAvailable:
			return strictJSON(msg.Payload, new(protocol.TaskAvailablePayload)) && cleanJSONStrings(msg.Payload)
		case protocol.EventDaemonRuntimeProfilesChanged:
			return strictJSON(msg.Payload, new(protocol.RuntimeProfilesChangedPayload)) && cleanJSONStrings(msg.Payload)
		case protocol.EventDaemonPendingWork:
			return strictJSON(msg.Payload, new(protocol.PendingWorkPayload)) && cleanJSONStrings(msg.Payload)
		case protocol.EventDaemonWorkspacesChanged, protocol.EventDaemonDingTalkPersonalMessageAvailable:
			return strictJSON(msg.Payload, new(struct{}))
		default:
			return false
		}
	}
	switch msg.Type {
	case protocol.EventTaskProgress:
		return strictJSON(msg.Payload, new(protocol.TaskProgressPayload)) && cleanJSONStrings(msg.Payload)
	case protocol.EventTaskMessage:
		// Only plain text, never opaque tool input/output. These fields match
		// both the present workspace projection and the scoped text payload.
		var p struct {
			TaskID    string `json:"task_id"`
			IssueID   string `json:"issue_id"`
			Seq       int    `json:"seq"`
			Type      string `json:"type"`
			Content   string `json:"content"`
			CreatedAt string `json:"created_at"`
			CallID    string `json:"call_id"`
		}
		return strictJSON(msg.Payload, &p) && p.Type == "text" && cleanJSONStrings(msg.Payload)
	case protocol.EventChatMessage:
		return strictJSON(msg.Payload, new(protocol.ChatMessagePayload)) && cleanJSONStrings(msg.Payload)
	case protocol.EventChatDone:
		return strictJSON(msg.Payload, new(protocol.ChatDonePayload)) && cleanJSONStrings(msg.Payload)
	case protocol.EventChatQuickActions:
		return strictJSON(msg.Payload, new(protocol.ChatQuickActionsPayload)) && cleanJSONStrings(msg.Payload)
	case protocol.EventInboxBatchRead, protocol.EventInboxBatchArchived:
		var p struct {
			RecipientID string   `json:"recipient_id"`
			Count       int      `json:"count"`
			IDs         []string `json:"ids"`
			ItemIDs     []string `json:"item_ids"`
		}
		return strictJSON(msg.Payload, &p) && cleanJSONStrings(msg.Payload)
	default:
		return false
	}
}

// Conservatively skip free text containing URLs, embedded media, or common
// credential markers. Schema review is the primary boundary: this is extra
// protection, not a claim to discover arbitrary user-pasted secrets. No
// substring matching alone can prove arbitrary free text is secret-free.
func cleanText(values ...string) bool {
	for _, value := range values {
		v := strings.ToLower(value)
		for _, marker := range []string{"http:", "https:", "data:", "bearer ", "-----begin ", "auth_token", "api_key", "api-key", "access_token", "secret", "password", "signature=", "policy="} {
			if strings.Contains(v, marker) {
				return false
			}
		}
	}
	return true
}

func cleanJSONStrings(data []byte) bool {
	// The schema decoder has already validated JSON. Walk its string tokens
	// directly to avoid allocating a second tree or decoder.Token interfaces
	// for large ID lists. Decode escapes before applying the same checks.
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		start := i
		escaped, closed := false, false
		for i++; i < len(data); i++ {
			if data[i] == '\\' {
				escaped = true
				i++
				continue
			}
			if data[i] == '"' {
				closed = true
				break
			}
		}
		if !closed {
			return false
		}
		if escaped {
			var value string
			if json.Unmarshal(data[start:i+1], &value) != nil || !cleanText(value) {
				return false
			}
		} else if !cleanText(string(data[start+1 : i])) {
			return false
		}
	}
	return true
}

// ClientMessageAllowed audits the actual Go daemon's outgoing request schemas.
// Responses from tasks.claim remain excluded by the server-side allowlist.
func ClientMessageAllowed(data []byte) bool {
	var msg protocol.Message
	if !strictJSON(data, &msg) {
		return false
	}
	switch msg.Type {
	case protocol.EventDaemonHeartbeat:
		return strictJSON(msg.Payload, new(protocol.DaemonHeartbeatRequestPayload)) && cleanJSONStrings(msg.Payload)
	case protocol.EventDaemonRPCRequest:
		var p protocol.RPCRequestPayload
		if !strictJSON(msg.Payload, &p) || p.Method != "tasks.claim" {
			return false
		}
		var body struct {
			DaemonID   string   `json:"daemon_id"`
			RuntimeIDs []string `json:"runtime_ids"`
			MaxTasks   int      `json:"max_tasks"`
		}
		return strictJSON(p.Body, &body) && cleanJSONStrings(msg.Payload)
	default:
		return false
	}
}
