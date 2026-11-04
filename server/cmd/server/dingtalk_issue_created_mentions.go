package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/util"
)

// Only the post-commit creation snapshot is used here. In particular, a
// minimal creation event must never reload a subsequently edited description.
func (r *dingtalkNotifyRuntime) enqueueIssueCreatedDescriptionMentions(issue dingtalkAssignmentIssue) {
	ctx := context.Background()
	senderID := r.personalMentionSenderUserID(issue.workspaceID, issue.actorType, issue.actorID)
	personalEnabled := false
	// Share assignment creation keys so a recipient has one creation delivery
	// per channel even when an event replay takes a different notification path.
	event := notify.MentionCreated{
		EventID: "issue-assignment:" + issue.id + ":created:assignment_added", WorkspaceID: issue.workspaceID,
		Actor: notify.Actor{ID: issue.actorID, Kind: issue.actorType, Name: issue.actorName},
		Text:  issue.description, IssueIdentifier: issue.identifier, IssueTitle: issue.title,
		CreatedAt: issue.occurredAt,
	}
	for _, mention := range util.ParseMentions(issue.description) {
		if mention.Type != "member" || mention.ID == issue.actorID || mention.ID == senderID {
			continue
		}
		// The assignee path merges this body into its existing creation message.
		if issue.assigneeType == "member" && mention.ID == issue.assigneeID {
			continue
		}
		// Description messages contain the task body, so fail closed on access errors.
		if r.memberInWorkspace == nil || r.canViewIssue == nil {
			continue
		}
		member, err := r.memberInWorkspace(ctx, issue.workspaceID, mention.ID)
		if err != nil || !member {
			slog.Info("dingtalk created description: recipient membership unavailable", "issue_id", issue.id, "recipient_id", mention.ID, "error", err)
			continue
		}
		allowed, err := r.canViewIssue(ctx, issue.workspaceID, mention.ID, issue.id)
		if err != nil || !allowed {
			slog.Info("dingtalk created description: recipient cannot view issue", "issue_id", issue.id, "recipient_id", mention.ID, "error", err)
			continue
		}
		event.Targets = append(event.Targets, notify.MentionTarget{ID: mention.ID, Kind: "member"})
	}
	if len(event.Targets) == 0 {
		return
	}
	if data := r.loadMentionContext(ctx, issue.id, issue.workspaceID, issue.actorType, issue.actorID, ""); data != nil {
		event.WorkspaceName, event.ProjectName, event.SourceURL = data.workspaceName, data.projectName, data.sourceURL
	}
	if senderID != "" {
		personalEnabled = r.issueCreatedPersonalMentionsEnabled(ctx, issue.workspaceID, senderID)
	}
	if personalEnabled {
		markdown := "**任务创建时在正文中提到了你**\n\n" + notify.FormatPersonalMentionText(event)
		enqueued := false
		for _, target := range event.Targets {
			key := fmt.Sprintf("dingtalk-personal-assignment:%s:created:%s", issue.id, target.ID)
			inserted, err := r.insertPersonalIssueNotification(ctx, issue, senderID, target.ID, markdown, key)
			if err != nil {
				slog.Warn("dingtalk created description: personal enqueue failed", "issue_id", issue.id, "recipient_id", target.ID, "error", err)
			} else if !inserted {
				reason, diagnosisErr := r.personalMentionEnqueueSkipReason(issue.workspaceID, key, senderID, target.ID)
				slog.Info("dingtalk created description: personal enqueue skipped", "issue_id", issue.id, "recipient_id", target.ID, "reason", reason, "error", diagnosisErr)
			}
			enqueued = enqueued || inserted
		}
		if enqueued && r.personalWakeup != nil {
			r.personalWakeup.NotifyDingTalkPersonalMessageAvailable(senderID)
		}
	}
	if r.store == nil || r.resolver == nil {
		return
	}
	messages, failures, err := notify.BuildMessagesWithOptions(ctx, event, r.resolver, notify.RoutingOptions{MemberP2POnly: true})
	if err != nil {
		slog.Warn("dingtalk created description: robot route failed", "issue_id", issue.id, "error", err)
		return
	}
	for _, failure := range failures {
		slog.Info("dingtalk created description: robot target skipped", "issue_id", issue.id, "recipient_id", failure.Message.TargetID, "reason", failure.Error)
	}
	if err := notify.EnqueueMessages(ctx, r.store, messages, time.Now().UTC()); err != nil {
		slog.Warn("dingtalk created description: robot enqueue failed", "issue_id", issue.id, "error", err)
	}
}

func (r *dingtalkNotifyRuntime) issueCreatedPersonalMentionsEnabled(ctx context.Context, workspaceID, senderID string) bool {
	if r.pool == nil {
		return false
	}
	var enabled bool
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(
		    (SELECT preferences->>'dingtalk_personal_mentions'
		     FROM notification_preference WHERE workspace_id = $1 AND user_id = $2),
		    'all'
		) <> 'muted'`, workspaceID, senderID).Scan(&enabled)
	if err != nil {
		slog.Warn("dingtalk created description: preference lookup failed", "workspace_id", workspaceID, "sender_id", senderID, "error", err)
	}
	return err == nil && enabled
}
