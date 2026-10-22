package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-20 coder(lq): Permission requests use the existing robot outbox and
// worker, not an inbox event listener: commit/rollback covers every channel.
func enqueueAccessRequestRobot(ctx context.Context, tx pgx.Tx, item issueAccessRequestResponse, recipientID, event, markdown string) error {
	config := notify.ConfigFromEnv(os.Getenv)
	if missing := config.MissingNotificationSettings(); len(missing) != 0 {
		slog.InfoContext(ctx, "task access request robot notification skipped: configuration incomplete", "missing", missing)
		return nil
	}
	var dingUserID string
	err := tx.QueryRow(ctx, `SELECT d.ding_user_id FROM dingtalk_notify_identities d
		WHERE d.multica_user_id=$2::uuid::text AND d.active=true AND d.login_only=false
		  AND COALESCE(d.ding_user_id,'')<>''
		  AND EXISTS (SELECT 1 FROM member m JOIN "user" u ON u.id=m.user_id
		              WHERE m.workspace_id=$1::uuid AND m.user_id=$2::uuid AND u.account_disabled_at IS NULL)
		ORDER BY d.updated_at DESC LIMIT 1`, item.WorkspaceID, recipientID).Scan(&dingUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.InfoContext(ctx, "task access request robot notification skipped: no active recipient binding", "workspace_id", item.WorkspaceID, "recipient_user_id", recipientID)
		return nil
	}
	if err != nil {
		return err
	}
	message := notify.Message{
		EventID: "task-access-request:" + item.ID + ":" + event, WorkspaceID: item.WorkspaceID,
		TargetID: recipientID, TargetKind: "member", DingUserID: dingUserID, ChannelType: "p2p", Text: markdown,
	}
	result, err := tx.Exec(ctx, `INSERT INTO dingtalk_notify_outbox
		(idempotency_key,workspace_id,event_id,target_id,target_kind,ding_user_id,channel_type,message_text,
		 status,attempts,next_attempt_at,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',0,now(),now(),now())
		ON CONFLICT (idempotency_key) DO NOTHING`, notify.IdempotencyKey(message), message.WorkspaceID,
		message.EventID, message.TargetID, message.TargetKind, message.DingUserID, message.ChannelType, message.Text)
	if err == nil && result.RowsAffected() > 0 {
		slog.InfoContext(ctx, "task access request robot notification staged", "workspace_id", item.WorkspaceID, "request_id", item.ID, "recipient_user_id", recipientID, "event", event)
	}
	return err
}

// 2026-10-20 coder(lq): Isolate optional channels with separate savepoints so
// either outbox (or failure-audit) can fail without cancelling the application.
func (h *Handler) enqueueAccessRequestDingTalkChannel(ctx context.Context, tx pgx.Tx, item issueAccessRequestResponse, recipientID, senderID, channel string, enqueue func() error) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT access_request_dingtalk`); err != nil {
		return err
	}
	if err := enqueue(); err != nil {
		if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT access_request_dingtalk`); rollbackErr != nil {
			return fmt.Errorf("rollback task access request %s enqueue after %v: %w", channel, err, rollbackErr)
		}
		slog.WarnContext(ctx, "task access request DingTalk enqueue failed", "channel", channel, "request_id", item.ID, "recipient_user_id", recipientID, "error", err)
		auditErr := (&projectAuthRepository{db: tx}).RecordAuthorizationAudit(ctx, projectauth.AuthorizationAuditEvent{
			WorkspaceID: item.WorkspaceID, IssueID: item.IssueID, ActorUserID: senderID,
			Action:  "task_access_request_dingtalk_enqueue_failed",
			Details: map[string]any{"request_id": item.ID, "recipient_user_id": recipientID, "channel": channel, "error": err.Error()},
		})
		if auditErr != nil {
			slog.ErrorContext(ctx, "record task access request DingTalk enqueue failure", "channel", channel, "request_id", item.ID, "error", auditErr)
			if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT access_request_dingtalk`); rollbackErr != nil {
				return fmt.Errorf("rollback task access request %s enqueue audit: %w", channel, rollbackErr)
			}
		}
	}
	_, err := tx.Exec(ctx, `RELEASE SAVEPOINT access_request_dingtalk`)
	return err
}
