package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type dingtalkPersonalMessageClaimRequest struct {
	DaemonID     string `json:"daemon_id"`
	RetryWaiting bool   `json:"retry_waiting"`
}

type dingtalkPersonalMessageClaim struct {
	ID                  string `json:"id"`
	WorkspaceID         string `json:"workspace_id"`
	SenderDingUserID    string `json:"sender_ding_user_id"`
	SenderUnionID       string `json:"sender_union_id,omitempty"`
	SenderCorpID        string `json:"sender_corp_id,omitempty"`
	RecipientDingUserID string `json:"recipient_ding_user_id"`
	Markdown            string `json:"markdown"`
	IdempotencyKey      string `json:"idempotency_key"`
	DWSOpenTaskID       string `json:"dws_open_task_id,omitempty"`
	ExpiresAt           string `json:"expires_at"`
}

type dingtalkPersonalMessageClaimResponse struct {
	Message *dingtalkPersonalMessageClaim `json:"message"`
}

// ClaimDingTalkPersonalMessage atomically leases one account-scoped mention
// notification. The WebSocket only carries a wakeup hint; message content is
// returned exclusively through this authenticated pull endpoint.
func (h *Handler) ClaimDingTalkPersonalMessage(w http.ResponseWriter, r *http.Request) {
	if !requestHasClientCapability(r, protocol.DaemonCapabilityDingTalkPersonalMessageV1) {
		writeError(w, http.StatusUpgradeRequired, "daemon does not support DingTalk personal messages")
		return
	}
	userID := strings.TrimSpace(requestUserID(r))
	if userID == "" {
		writeError(w, http.StatusForbidden, "DingTalk personal messages require user-authenticated daemon access")
		return
	}
	var req dingtalkPersonalMessageClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.DaemonID = strings.TrimSpace(req.DaemonID)
	if req.DaemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}

	if _, err := h.DB.Exec(r.Context(), `
		UPDATE dingtalk_personal_message
		SET status = 'expired', lease_owner = NULL, leased_until = NULL,
		    updated_at = now(), last_error_code = 'expired',
		    last_error_message = 'message expired before local DWS delivery'
		WHERE sender_user_id = $1
		  AND status IN ('pending', 'leased', 'waiting_for_dws_login', 'waiting_for_identity')
		  AND expires_at <= now()`, userID); err != nil {
		slog.Warn("expire DingTalk personal messages failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to claim DingTalk personal message")
		return
	}
	if req.RetryWaiting {
		if _, err := h.DB.Exec(r.Context(), `
			UPDATE dingtalk_personal_message
			SET available_at = now(), updated_at = now()
			WHERE sender_user_id = $1
			  AND status IN ('waiting_for_dws_login', 'waiting_for_identity')
			  AND expires_at > now()`, userID); err != nil {
			slog.Warn("release DingTalk personal messages after DWS login failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to retry DingTalk personal messages")
			return
		}
	}

	var message dingtalkPersonalMessageClaim
	var senderUnionID, senderCorpID, openTaskID *string
	var expiresAt time.Time
	err := h.DB.QueryRow(r.Context(), `
		UPDATE dingtalk_personal_message
		SET status = 'leased', attempts = attempts + 1,
		    lease_owner = $2, leased_until = now() + interval '2 minutes',
		    updated_at = now()
		WHERE id = (
		    SELECT id
		    FROM dingtalk_personal_message
		    WHERE sender_user_id = $1
		      AND expires_at > now()
		      AND available_at <= now()
		      AND (
		        status IN ('pending', 'waiting_for_dws_login', 'waiting_for_identity')
		        OR (status = 'leased' AND leased_until < now())
		      )
		    ORDER BY created_at ASC
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		)
		RETURNING id::text, workspace_id::text, sender_ding_user_id,
		          sender_union_id, sender_corp_id, recipient_ding_user_id,
		          markdown, idempotency_key, dws_open_task_id, expires_at`,
		userID, req.DaemonID).Scan(
		&message.ID, &message.WorkspaceID, &message.SenderDingUserID,
		&senderUnionID, &senderCorpID, &message.RecipientDingUserID,
		&message.Markdown, &message.IdempotencyKey, &openTaskID, &expiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, dingtalkPersonalMessageClaimResponse{})
		return
	}
	if err != nil {
		slog.Warn("claim DingTalk personal message failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to claim DingTalk personal message")
		return
	}
	if senderUnionID != nil {
		message.SenderUnionID = strings.TrimSpace(*senderUnionID)
	}
	if senderCorpID != nil {
		message.SenderCorpID = strings.TrimSpace(*senderCorpID)
	}
	if openTaskID != nil {
		message.DWSOpenTaskID = strings.TrimSpace(*openTaskID)
	}
	message.ExpiresAt = expiresAt.UTC().Format(time.RFC3339Nano)
	writeJSON(w, http.StatusOK, dingtalkPersonalMessageClaimResponse{Message: &message})
}

type dingtalkPersonalMessageResultRequest struct {
	DaemonID         string `json:"daemon_id"`
	Status           string `json:"status"`
	DWSOpenTaskID    string `json:"dws_open_task_id,omitempty"`
	DWSOpenMessageID string `json:"dws_open_message_id,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	ErrorMessage     string `json:"error_message,omitempty"`
}

// ReportDingTalkPersonalMessageResult settles or reschedules a lease. Login
// and identity failures remain queued until the 24-hour expiry so the member
// can repair local DWS state without losing the mention.
func (h *Handler) ReportDingTalkPersonalMessageResult(w http.ResponseWriter, r *http.Request) {
	if !requestHasClientCapability(r, protocol.DaemonCapabilityDingTalkPersonalMessageV1) {
		writeError(w, http.StatusUpgradeRequired, "daemon does not support DingTalk personal messages")
		return
	}
	userID := strings.TrimSpace(requestUserID(r))
	if userID == "" {
		writeError(w, http.StatusForbidden, "DingTalk personal messages require user-authenticated daemon access")
		return
	}
	messageID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	var req dingtalkPersonalMessageResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.DaemonID = strings.TrimSpace(req.DaemonID)
	req.Status = strings.TrimSpace(req.Status)
	if req.DaemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}
	if req.Status != "submitted" && req.Status != "delivered" && req.Status != "retry" &&
		req.Status != "waiting_for_dws_login" && req.Status != "waiting_for_identity" && req.Status != "failed" {
		writeError(w, http.StatusBadRequest, "invalid result status")
		return
	}
	req.DWSOpenTaskID = strings.TrimSpace(req.DWSOpenTaskID)
	req.DWSOpenMessageID = strings.TrimSpace(req.DWSOpenMessageID)
	if req.Status == "submitted" && req.DWSOpenTaskID == "" {
		writeError(w, http.StatusBadRequest, "dws_open_task_id is required for submitted results")
		return
	}
	if req.Status == "delivered" && (req.DWSOpenTaskID == "" || req.DWSOpenMessageID == "") {
		writeError(w, http.StatusBadRequest, "DWS task and message IDs are required for delivered results")
		return
	}

	status := req.Status
	availableDelay := time.Duration(0)
	switch status {
	case "submitted":
		status = "leased"
	case "retry":
		status = "pending"
		availableDelay = time.Minute
	case "waiting_for_dws_login", "waiting_for_identity":
		availableDelay = time.Minute
	}
	result, err := h.DB.Exec(r.Context(), `
		UPDATE dingtalk_personal_message
		SET status = CASE WHEN expires_at <= now() AND $4 <> 'delivered' THEN 'expired' ELSE $4 END,
		    available_at = now() + $5::interval,
		    lease_owner = CASE WHEN $4 = 'leased' THEN lease_owner ELSE NULL END,
		    leased_until = CASE WHEN $4 = 'leased' THEN now() + interval '2 minutes' ELSE NULL END,
		    dws_open_task_id = COALESCE(NULLIF($6, ''), dws_open_task_id),
		    dws_open_message_id = COALESCE(NULLIF($7, ''), dws_open_message_id),
		    last_error_code = NULLIF($8, ''),
		    last_error_message = NULLIF($9, ''),
		    delivered_at = CASE WHEN $4 = 'delivered' THEN now() ELSE delivered_at END,
		    updated_at = now()
		WHERE id = $1 AND sender_user_id = $2
		  AND status = 'leased' AND lease_owner = $3`,
		messageID, userID, req.DaemonID, status, availableDelay.String(),
		req.DWSOpenTaskID, req.DWSOpenMessageID,
		strings.TrimSpace(req.ErrorCode), strings.TrimSpace(req.ErrorMessage))
	if err != nil {
		slog.Warn("report DingTalk personal message failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update DingTalk personal message")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "DingTalk personal message lease is no longer active")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
