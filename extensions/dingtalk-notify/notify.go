// Package notify contains the isolated DingTalk mention notification contract.
// It deliberately depends on interfaces and standard library types only.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Actor struct {
	ID   string
	Name string
	Kind string // member or agent
}

type MentionTarget struct {
	ID   string
	Kind string // member or agent
}

type MentionCreated struct {
	EventID     string
	WorkspaceID string
	Actor       Actor
	Targets     []MentionTarget
	Text        string
	SourceURL   string
	// Context fields are optional enrichment supplied by the host adapter. The
	// notification module keeps them independent from Multica's database types
	// so future event sources can reuse the same formatter.
	WorkspaceName   string
	ProjectName     string
	IssueIdentifier string
	IssueTitle      string
	CreatedAt       time.Time
}

type MemberBinding struct {
	WorkspaceID string
	MemberID    string
	DingUserID  string
	UnionID     string
	OpenID      string
	RobotCode   string
	Active      bool
	// Groups are optional member-owned group targets. An empty list keeps the
	// default P2P behaviour; a group-intent message uses matching groups (or
	// all groups when the intent is generic).
	Groups []AgentChannel
}

type AgentChannel struct {
	WorkspaceID string
	AgentID     string
	ChannelID   string
	ChannelName string
	RobotCode   string
	OwnerID     string
	Active      bool
}

type Resolver interface {
	MemberBinding(ctx context.Context, workspaceID, memberID string) (MemberBinding, bool, error)
	AgentChannels(ctx context.Context, workspaceID, agentID string) ([]AgentChannel, error)
}

type Message struct {
	EventID     string
	WorkspaceID string
	TargetID    string
	TargetKind  string
	ChannelID   string
	RobotCode   string
	DingUserID  string
	ChannelType string // p2p or group
	Text        string
}

type Provider interface {
	Send(ctx context.Context, message Message) error
}

// RoutingOptions controls optional target types. Agent notifications are
// deliberately disabled by default for the first rollout: an Agent may only
// notify DingTalk after its own Bot and destination group have been explicitly
// configured and the feature is enabled by the host.
type RoutingOptions struct {
	EnableAgentNotifications bool
}

type Delivery struct {
	Message Message
	Status  string // delivered, failed, skipped
	Error   string
}

// BuildMessages applies the agreed routing rules. Members default to P2P;
// agents only use their configured bot channels. A group is selected only
// when the text explicitly names the channel or asks to send to a group.
func BuildMessages(ctx context.Context, event MentionCreated, resolver Resolver) ([]Message, []Delivery, error) {
	return BuildMessagesWithOptions(ctx, event, resolver, RoutingOptions{})
}

// BuildMessagesWithOptions applies the routing rules for the enabled target
// types. Members default to P2P. Agent routing remains opt-in until the host
// enables the deferred Agent notification feature.
func BuildMessagesWithOptions(ctx context.Context, event MentionCreated, resolver Resolver, options RoutingOptions) ([]Message, []Delivery, error) {
	if event.EventID == "" || event.WorkspaceID == "" {
		return nil, nil, errors.New("event_id and workspace_id are required")
	}
	if resolver == nil {
		return nil, nil, errors.New("mention resolver is required")
	}
	text := strings.TrimSpace(event.Text)
	var messages []Message
	var failures []Delivery
	for _, target := range event.Targets {
		switch target.Kind {
		case "member":
			binding, found, err := resolver.MemberBinding(ctx, event.WorkspaceID, target.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve member %s: %w", target.ID, err)
			}
			if !found || !binding.Active || binding.DingUserID == "" {
				failures = append(failures, failed(event, target, "member is not bound to DingTalk"))
				continue
			}
			groupIntent := groupRequested(text, binding.Groups)
			if groupIntent {
				matched := 0
				seen := map[string]struct{}{}
				for _, group := range binding.Groups {
					if !group.Active || group.ChannelID == "" || (!genericGroupIntent(text) && !strings.Contains(text, group.ChannelName)) {
						continue
					}
					if _, ok := seen[group.ChannelID]; ok {
						continue
					}
					seen[group.ChannelID] = struct{}{}
					matched++
					messages = append(messages, Message{EventID: event.EventID, WorkspaceID: event.WorkspaceID, TargetID: target.ID, TargetKind: target.Kind, ChannelID: group.ChannelID, RobotCode: group.RobotCode, ChannelType: "group", Text: FormatText(event)})
				}
				if matched == 0 {
					failures = append(failures, failed(event, target, "member has no matching active DingTalk group"))
				}
				continue
			}
			messages = append(messages, Message{EventID: event.EventID, WorkspaceID: event.WorkspaceID, TargetID: target.ID, TargetKind: target.Kind, RobotCode: binding.RobotCode, DingUserID: binding.DingUserID, ChannelType: "p2p", Text: FormatText(event)})
		case "agent":
			if !options.EnableAgentNotifications {
				failures = append(failures, skipped(event, target, "agent DingTalk notifications are deferred"))
				continue
			}
			channels, err := resolver.AgentChannels(ctx, event.WorkspaceID, target.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve agent %s: %w", target.ID, err)
			}
			matched := 0
			seen := map[string]struct{}{}
			groupIntent := groupRequested(text, channels)
			genericIntent := genericGroupIntent(text)
			for _, channel := range channels {
				// A channel without its own robot code is not a valid Agent
				// destination. Never fall back to a deployment-wide/default Bot.
				if !channel.Active || channel.ChannelID == "" || channel.RobotCode == "" || (groupIntent && !genericIntent && !strings.Contains(text, channel.ChannelName)) || (!groupIntent && !strings.Contains(text, channel.ChannelName)) {
					continue
				}
				if _, ok := seen[channel.ChannelID]; ok {
					continue
				}
				seen[channel.ChannelID] = struct{}{}
				matched++
				messages = append(messages, Message{EventID: event.EventID, WorkspaceID: event.WorkspaceID, TargetID: target.ID, TargetKind: target.Kind, ChannelID: channel.ChannelID, RobotCode: channel.RobotCode, ChannelType: "group", Text: FormatText(event)})
			}
			if matched == 0 {
				failures = append(failures, failed(event, target, "agent has no matching active DingTalk channel"))
			}
		default:
			failures = append(failures, failed(event, target, "unsupported mention target"))
		}
	}
	return messages, failures, nil
}

func genericGroupIntent(text string) bool {
	return strings.Contains(text, "发群") || strings.Contains(text, "群里") || strings.Contains(text, "群消息")
}

func groupRequested(text string, groups []AgentChannel) bool {
	if genericGroupIntent(text) {
		return true
	}
	for _, group := range groups {
		if group.ChannelName != "" && strings.Contains(text, group.ChannelName) {
			return true
		}
	}
	return false
}

func failed(event MentionCreated, target MentionTarget, reason string) Delivery {
	return Delivery{Message: Message{EventID: event.EventID, WorkspaceID: event.WorkspaceID, TargetID: target.ID, TargetKind: target.Kind}, Status: "failed", Error: reason}
}

func skipped(event MentionCreated, target MentionTarget, reason string) Delivery {
	return Delivery{Message: Message{EventID: event.EventID, WorkspaceID: event.WorkspaceID, TargetID: target.ID, TargetKind: target.Kind}, Status: StatusSkipped, Error: reason}
}

func FormatText(event MentionCreated) string {
	text := sanitizeText(event.Text)
	if runes := []rune(text); len(runes) > 2000 {
		text = string(runes[:2000]) + "…"
	}

	actor := strings.TrimSpace(event.Actor.Name)
	if actor == "" {
		if event.Actor.Kind == "agent" {
			actor = "Multica Agent"
		} else {
			actor = "一位 Multica 成员"
		}
	}

	lines := []string{fmt.Sprintf("🔔 **%s 在 Multica 中提到了你**", escapeMarkdown(actor))}
	if source := notificationSource(event); source != "" {
		lines = append(lines, "来源："+source)
	}
	if task := notificationTask(event); task != "" {
		lines = append(lines, "任务："+task)
	}
	if text != "" {
		lines = append(lines, "消息：\n"+quoteMarkdown(text))
	}
	if event.SourceURL != "" {
		// Keep the reply action visually separate from the quoted message. A
		// heading gives DingTalk's Markdown renderer a larger, more discoverable
		// action label while the arrow makes it clear that this is a link.
		lines = append(lines, "---", "### ↗️ [打开任务并回复]("+event.SourceURL+")")
	}
	return strings.Join(lines, "\n\n")
}

func notificationSource(event MentionCreated) string {
	workspace := strings.TrimSpace(event.WorkspaceName)
	project := strings.TrimSpace(event.ProjectName)
	if workspace == "" {
		return escapeMarkdown(project)
	}
	if project == "" {
		return escapeMarkdown(workspace)
	}
	return escapeMarkdown(workspace) + " / " + escapeMarkdown(project)
}

func notificationTask(event MentionCreated) string {
	identifier := strings.TrimSpace(event.IssueIdentifier)
	title := strings.TrimSpace(event.IssueTitle)
	if identifier == "" {
		return escapeMarkdown(title)
	}
	label := escapeMarkdown(identifier)
	if title != "" {
		label += " · " + escapeMarkdown(title)
	}
	if event.SourceURL == "" {
		return label
	}
	return "[" + label + "](" + event.SourceURL + ")"
}

func quoteMarkdown(text string) string {
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		parts[i] = "> " + part
	}
	return strings.Join(parts, "\n")
}

// escapeMarkdown protects display-only values (names and titles). The body
// intentionally remains Markdown because Multica mention links are useful in
// the DingTalk card and are rendered as the human-readable mention label.
func escapeMarkdown(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch r {
		case '\\', '*', '_', '`', '[', ']', '<', '>':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sanitizeText(text string) string {
	text = strings.TrimSpace(text)
	return strings.Map(func(r rune) rune {
		if r == '\x00' || r == '\r' {
			return -1
		}
		return r
	}, text)
}

// Deliver sends each message independently. A failed target is returned to
// the caller for audit and retry; it never causes the originating comment to
// fail.
func Deliver(ctx context.Context, provider Provider, messages []Message) []Delivery {
	results := make([]Delivery, 0, len(messages))
	for _, message := range messages {
		err := provider.Send(ctx, message)
		result := Delivery{Message: message, Status: "delivered"}
		if err != nil {
			result.Status, result.Error = "failed", err.Error()
		}
		results = append(results, result)
	}
	return results
}
