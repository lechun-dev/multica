package handler

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 2026-10-19 coder(lq): Suspension changes only account access. Memberships,
// grants, assignments, comments and projects are intentionally left untouched.
func (h *Handler) SetMemberAccountAccess(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "only workspace owners can manage account access")
		return
	}
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	memberID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memberId"), "member id")
	if !ok {
		return
	}
	var input struct {
		Disabled *bool `json:"disabled"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Disabled == nil {
		writeError(w, http.StatusBadRequest, "disabled is required")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	target, err := h.Queries.GetMember(r.Context(), memberID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) || target.WorkspaceID != requester.WorkspaceID {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if target.UserID == requester.UserID {
		writeError(w, http.StatusBadRequest, "cannot disable your own account")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update account access")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// 2026-10-19 coder(lq): Serialize toggles on this account. Repeated identical requests are no-ops.
	var lockedID string
	if err := tx.QueryRow(r.Context(), `SELECT id::text FROM "user" WHERE id=$1 FOR UPDATE`, target.UserID).Scan(&lockedID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock account")
		return
	}
	// 2026-10-19 coder(lq): Hold membership rows while deciding global authority; role/removal writers
	// cannot change the same rows halfway through the account mutation.
	rows, err := tx.Query(r.Context(), `SELECT id FROM member WHERE user_id=$1 OR user_id=$2 ORDER BY id FOR UPDATE`, target.UserID, requester.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock memberships")
		return
	}
	for rows.Next() {
	} // Consume before the next command on this transaction.
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock memberships")
		return
	}
	target, err = qtx.GetMember(r.Context(), memberID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload member")
		return
	}
	allowed, err := qtx.ListManageableAccounts(r.Context(), db.ListManageableAccountsParams{WorkspaceID: requester.WorkspaceID, ActorID: requester.UserID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify account authority")
		return
	}
	manageable := false
	for _, id := range allowed {
		if id == target.UserID {
			manageable = true
			break
		}
	}
	if !manageable {
		writeError(w, http.StatusForbidden, "cannot manage an owner or an account outside your owned workspaces")
		return
	}
	user, err := qtx.GetUser(r.Context(), target.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}
	if auth.IsTemporarilyDisabledUser(lockedID, user.Email) {
		writeError(w, http.StatusConflict, "account is blocked by the instance security policy")
		return
	}
	var revokedHashes []string
	if user.AccountDisabledAt.Valid != *input.Disabled {
		state, err := qtx.SetUserAccountAccess(r.Context(), db.SetUserAccountAccessParams{ID: target.UserID, Disabled: *input.Disabled})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update account access")
			return
		}
		user.AccountDisabledAt, user.SessionVersion = state.AccountDisabledAt, state.SessionVersion
		if *input.Disabled {
			tokens, err := tx.Query(r.Context(), `UPDATE personal_access_token SET revoked=true WHERE user_id=$1 AND NOT revoked RETURNING token_hash`, target.UserID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to revoke account tokens")
				return
			}
			for tokens.Next() {
				var hash string
				if err = tokens.Scan(&hash); err != nil {
					break
				}
				revokedHashes = append(revokedHashes, hash)
			}
			scanErr := tokens.Err()
			tokens.Close()
			if err != nil || scanErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to revoke account tokens")
				return
			}
		}
	}
	var loggedIn bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM projectauth_user_logins WHERE user_id=$1)`, target.UserID).Scan(&loggedIn); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load member status")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update account access")
		return
	}
	for _, hash := range revokedHashes {
		h.PATCache.Invalidate(r.Context(), hash)
	}
	response := h.memberWithUserResponse(target, user)
	response.CanManageAccount = true
	response.HasLoggedIn = loggedIn || user.OnboardedAt.Valid || target.Role == "admin" || target.Role == "owner"
	slog.Info("member account access updated", "actor_id", requestUserID(r), "user_id", lockedID, "workspace_id", workspaceID, "disabled", *input.Disabled)
	h.publish(protocol.EventMemberUpdated, workspaceID, "member", requestUserID(r), map[string]any{"member": response})
	if *input.Disabled && h.Hub != nil {
		payload, _ := json.Marshal(map[string]any{"type": "account:disabled", "payload": map[string]string{"user_id": lockedID}})
		h.Hub.SendToUser(lockedID, payload)
	}
	writeJSON(w, http.StatusOK, response)
}
