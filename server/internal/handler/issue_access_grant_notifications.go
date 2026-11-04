package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

type issueDirectGrantChange struct {
	UserID  string
	Updated bool
	Grants  []issueAccessControlGrant
}

// Compare direct, active grants rather than effective access: an explicit share
// is meaningful even when project or organization permissions already allow it.
func changedIssueDirectGrants(before, after []issueAccessControlGrant, actorID string, now time.Time) []issueDirectGrantChange {
	group := func(grants []issueAccessControlGrant) map[string]map[projectauth.RoleKey]issueAccessControlGrant {
		users := map[string]map[projectauth.RoleKey]issueAccessControlGrant{}
		for _, grant := range grants {
			if grant.SubjectType != projectauth.SubjectUser || grant.SubjectID == actorID ||
				(grant.ExpiresAt != nil && !grant.ExpiresAt.After(now)) {
				continue
			}
			if users[grant.SubjectID] == nil {
				users[grant.SubjectID] = map[projectauth.RoleKey]issueAccessControlGrant{}
			}
			users[grant.SubjectID][grant.Role] = grant
		}
		return users
	}
	previous, next := group(before), group(after)
	var changes []issueDirectGrantChange
	for userID, roles := range next {
		change := issueDirectGrantChange{UserID: userID, Updated: len(previous[userID]) > 0}
		notifyUser := false
		for role, grant := range roles {
			old, existed := previous[userID][role]
			if !existed || !sameOptionalTime(old.ExpiresAt, grant.ExpiresAt) {
				notifyUser = true
			}
			change.Grants = append(change.Grants, grant)
		}
		// Pure removals are revocations, which do not send a grant notice.
		if notifyUser {
			sort.Slice(change.Grants, func(i, j int) bool { return change.Grants[i].Role < change.Grants[j].Role })
			changes = append(changes, change)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].UserID < changes[j].UserID })
	return changes
}

// Both outboxes are staged in the authorization transaction. Preview and failed
// commits roll back every intent; existing workers deliver only committed rows.
func (h *Handler) enqueueIssueAccessGrantNotifications(ctx context.Context, tx dbExecutor, actor projectauth.Subject, issueID string, version int64, before, after []issueAccessControlGrant) error {
	changes := changedIssueDirectGrants(before, after, actor.UserID, time.Now())
	if len(changes) == 0 {
		return nil
	}
	var identities, robotTable, personalTable bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('dingtalk_notify_identities') IS NOT NULL,
		to_regclass('dingtalk_notify_outbox') IS NOT NULL,to_regclass('dingtalk_personal_message') IS NOT NULL`).Scan(&identities, &robotTable, &personalTable); err != nil {
		return err
	}
	config := notify.ConfigFromEnv(os.Getenv)
	robotEnabled := robotTable && len(config.MissingNotificationSettings()) == 0
	if !identities || (!robotEnabled && !personalTable) {
		slog.InfoContext(ctx, "task access grant DingTalk notification skipped: integration unavailable", "issue_id", issueID)
		return nil
	}
	var actorName, title, slug, prefix string
	var number int32
	if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(u.name,''),NULLIF(u.email,''),'工作区成员'),i.title,w.slug,w.issue_prefix,i.number
		FROM issue i JOIN workspace w ON w.id=i.workspace_id LEFT JOIN "user" u ON u.id=$3::uuid
		WHERE i.workspace_id=$1::uuid AND i.id=$2::uuid`, actor.WorkspaceID, issueID, actor.UserID).
		Scan(&actorName, &title, &slug, &prefix, &number); err != nil {
		return err
	}
	roleNames := map[projectauth.RoleKey]string{}
	for _, change := range changes {
		for _, grant := range change.Grants {
			if _, exists := roleNames[grant.Role]; exists {
				continue
			}
			name := issueAccessRoleLabel(grant.Role)
			if !projectauth.IsSystemTaskRole(projectauth.TaskRole(grant.Role)) {
				if err := tx.QueryRow(ctx, `SELECT name FROM projectauth_task_roles WHERE workspace_id=$1 AND role_key=$2`, actor.WorkspaceID, grant.Role).Scan(&name); err != nil {
					return err
				}
			}
			roleNames[grant.Role] = name
		}
	}
	sourceURL := ""
	identifier := service.IssueIdentifier(prefix, number)
	if base := strings.TrimRight(strings.TrimSpace(h.cfg.AppURL), "/"); base != "" {
		sourceURL = base + "/" + url.PathEscape(slug) + "/issues/" + url.PathEscape(identifier)
	}
	resolver := projectauth.NewEffectiveAccessResolver(&projectAuthRepository{db: tx})
	for _, change := range changes {
		var dingUserID string
		err := tx.QueryRow(ctx, `SELECT d.ding_user_id FROM dingtalk_notify_identities d
			JOIN member m ON m.workspace_id=$1::uuid AND m.user_id=$2::uuid
			JOIN "user" u ON u.id=m.user_id AND u.account_disabled_at IS NULL
			WHERE d.multica_user_id=m.user_id::text AND d.active=true AND d.login_only=false AND COALESCE(d.ding_user_id,'')<>''
			ORDER BY d.updated_at DESC LIMIT 1`, actor.WorkspaceID, change.UserID).Scan(&dingUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			slog.InfoContext(ctx, "task access grant DingTalk notification skipped: recipient unavailable", "issue_id", issueID, "recipient_user_id", change.UserID)
			continue
		}
		if err != nil {
			return err
		}
		explanation, err := resolver.ExplainIssue(ctx, projectauth.Subject{WorkspaceID: actor.WorkspaceID, UserID: change.UserID}, issueID, projectauth.View)
		if err != nil {
			return err
		}
		if !explanation.Allowed {
			slog.InfoContext(ctx, "task access grant DingTalk notification skipped: task access denied", "issue_id", issueID, "recipient_user_id", change.UserID)
			continue
		}
		notification := notify.AccessGrantNotification{ActorName: actorName, Updated: change.Updated,
			IssueIdentifier: identifier, IssueTitle: title, SourceURL: sourceURL}
		for _, grant := range change.Grants {
			notification.Permissions = append(notification.Permissions, notify.AccessGrantPermission{RoleName: roleNames[grant.Role], ExpiresAt: grant.ExpiresAt})
		}
		markdown := notify.FormatAccessGrantText(notification)
		eventID := fmt.Sprintf("task-access-grant:%s:%d", issueID, version)
		if robotEnabled {
			message := notify.Message{EventID: eventID, WorkspaceID: actor.WorkspaceID, TargetID: change.UserID,
				TargetKind: "member", DingUserID: dingUserID, ChannelType: "p2p", Text: markdown}
			if _, err := tx.Exec(ctx, `INSERT INTO dingtalk_notify_outbox
				(idempotency_key,workspace_id,event_id,target_id,target_kind,ding_user_id,channel_type,message_text,
				status,attempts,next_attempt_at,created_at,updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',0,now(),now(),now()) ON CONFLICT (idempotency_key) DO NOTHING`,
				notify.IdempotencyKey(message), message.WorkspaceID, message.EventID, message.TargetID,
				message.TargetKind, message.DingUserID, message.ChannelType, message.Text); err != nil {
				return err
			}
		}
		if personalTable {
			result, err := tx.Exec(ctx, `INSERT INTO dingtalk_personal_message
				(workspace_id,comment_id,sender_user_id,sender_ding_user_id,sender_union_id,sender_corp_id,
				recipient_user_id,recipient_ding_user_id,markdown,idempotency_key)
				SELECT $1::uuid,$2::uuid,$3::uuid,COALESCE(d.ding_user_id,''),NULLIF(d.union_id,''),NULL,$4::uuid,$5,$6,$7
				FROM dingtalk_notify_identities d JOIN member m ON m.workspace_id=$1::uuid AND m.user_id=$3::uuid
				JOIN "user" u ON u.id=m.user_id AND u.account_disabled_at IS NULL
				WHERE d.multica_user_id=m.user_id::text AND d.active=true
				AND (COALESCE(d.union_id,'')<>'' OR COALESCE(d.ding_user_id,'')<>'')
				ORDER BY d.updated_at DESC LIMIT 1 ON CONFLICT (idempotency_key) DO NOTHING`, actor.WorkspaceID, issueID,
				actor.UserID, change.UserID, dingUserID, markdown, eventID+":"+change.UserID+":personal")
			if err != nil {
				return err
			}
			if result.RowsAffected() == 0 {
				slog.InfoContext(ctx, "task access grant personal notification skipped: sender unavailable or duplicate", "issue_id", issueID, "recipient_user_id", change.UserID)
			}
		}
	}
	return nil
}
