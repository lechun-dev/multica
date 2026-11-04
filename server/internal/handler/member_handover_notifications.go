package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/auth"
)

type handoverNotificationPayload struct {
	HandoverID string `json:"handover_id,omitempty"`
	Text       string `json:"text,omitempty"`
}

func handoverAppLink(slug, path string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/")
	if base == "" {
		return ""
	}
	return base + "/" + url.PathEscape(slug) + path
}
func escapeHandoverMarkdown(s string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "\n", " ", "\r", " ").Replace(s)
}
func stageHandoverNotification(ctx context.Context, tx pgx.Tx, ws, recipient, event, issue string, payload handoverNotificationPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO member_handover_notifications(event_id,workspace_id,recipient_user_id,issue_id,handover_id,payload)
  VALUES($1,$2,$3::uuid,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6::jsonb) ON CONFLICT(event_id,recipient_user_id) DO NOTHING`, event, ws, recipient, issue, payload.HandoverID, body)
	return err
}

// FlushMemberHandoverNotifications bridges durable intent into the existing
// Mission outbox. Both the insert and the staged marker commit together; failed
// routing remains pending and retries independently of business handover.
func FlushMemberHandoverNotifications(ctx context.Context, starter interface {
	Begin(context.Context) (pgx.Tx, error)
}, canViewIssue func(context.Context, string, string, string) (bool, error)) error {
	if len(notify.ConfigFromEnv(os.Getenv).MissingNotificationSettings()) > 0 {
		return nil
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT event_id,workspace_id::text,recipient_user_id::text,COALESCE(issue_id::text,''),payload FROM member_handover_notifications
  WHERE status='pending' AND next_attempt_at<=now() ORDER BY next_attempt_at,event_id LIMIT 25 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	type intent struct {
		event, ws, recipient, issue string
		body                        []byte
	}
	var batch []intent
	for rows.Next() {
		var i intent
		if err = rows.Scan(&i.event, &i.ws, &i.recipient, &i.issue, &i.body); err != nil {
			break
		}
		batch = append(batch, i)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range batch {
		var payload handoverNotificationPayload
		if err = json.Unmarshal(i.body, &payload); err != nil {
			return err
		}

		var dingID, email string
		routeErr := tx.QueryRow(ctx, `SELECT COALESCE(d.ding_user_id,''),u.email FROM member m JOIN "user" u ON u.id=m.user_id
   LEFT JOIN LATERAL (SELECT d.ding_user_id FROM dingtalk_notify_identities d WHERE d.multica_user_id=m.user_id::text
    AND d.active AND NOT d.login_only AND COALESCE(d.ding_user_id,'')<>'' ORDER BY d.updated_at DESC LIMIT 1) d ON true
   WHERE m.workspace_id=$1 AND m.user_id=$2::uuid AND u.account_disabled_at IS NULL`, i.ws, i.recipient).Scan(&dingID, &email)
		reason := ""
		if errors.Is(routeErr, pgx.ErrNoRows) {
			reason = "recipient_unavailable"
		} else if routeErr != nil {
			return routeErr
		} else if auth.IsTemporarilyDisabledUser(i.recipient, email) {
			reason = "recipient_unavailable"
		} else if dingID == "" {
			reason = "recipient_unbound"
		}
		if reason == "" && i.issue != "" {
			allowed := false
			if canViewIssue != nil {
				allowed, err = canViewIssue(ctx, i.ws, i.recipient, i.issue)
				if err != nil {
					return err
				}
			}
			if !allowed {
				reason = "checklist_access_unavailable"
			}
		}
		if reason != "" {
			if _, err = tx.Exec(ctx, `UPDATE member_handover_notifications SET attempts=attempts+1,last_error=$3,next_attempt_at=now()+interval '5 minutes' WHERE event_id=$1 AND recipient_user_id=$2::uuid`, i.event, i.recipient, reason); err != nil {
				return err
			}
			continue
		}
		message := notify.Message{EventID: i.event, WorkspaceID: i.ws, TargetID: i.recipient, TargetKind: "member", DingUserID: dingID, ChannelType: "p2p", Text: payload.Text}
		if _, err = tx.Exec(ctx, `INSERT INTO dingtalk_notify_outbox(idempotency_key,workspace_id,event_id,target_id,target_kind,ding_user_id,channel_type,message_text,status,attempts,next_attempt_at,created_at,updated_at)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',0,now(),now(),now()) ON CONFLICT(idempotency_key) DO NOTHING`, notify.IdempotencyKey(message), i.ws, i.event, i.recipient, "member", dingID, "p2p", payload.Text); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE member_handover_notifications SET status='staged',last_error='',staged_at=now() WHERE event_id=$1 AND recipient_user_id=$2::uuid`, i.event, i.recipient); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
