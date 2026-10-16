package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ExchangeDingTalkReadToken is mounted only behind MissionExchangeAuth.
// IDs originate from the trusted caller-key gateway, never the external caller.
func (h *Handler) ExchangeDingTalkReadToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DingUserID  string `json:"ding_user_id"`
		WorkspaceID string `json:"workspace_id"`
		Purpose     string `json:"purpose"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.DingUserID) == "" || len(input.DingUserID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid identity exchange request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid identity exchange request")
		return
	}
	if purpose := strings.TrimSpace(input.Purpose); purpose != "" && purpose != "agent_task_read" {
		writeError(w, http.StatusBadRequest, "invalid identity exchange purpose")
		return
	}
	ws, ok := parseUUIDOrBadRequest(w, input.WorkspaceID, "workspace_id")
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT DISTINCT multica_user_id
FROM dingtalk_notify_identities WHERE active = true AND ding_user_id = $1
AND multica_user_id <> '' LIMIT 2`, input.DingUserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "identity mapping unavailable")
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "identity mapping unavailable")
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "identity mapping unavailable")
		return
	}
	if len(ids) == 0 {
		writeError(w, http.StatusForbidden, "mission identity not bound")
		return
	}
	if len(ids) != 1 {
		writeError(w, http.StatusConflict, "mission identity mapping ambiguous")
		return
	}
	userID, err := util.ParseUUID(ids[0])
	if err != nil {
		writeError(w, http.StatusConflict, "mission identity mapping invalid")
		return
	}
	user, err := h.Queries.GetUser(r.Context(), userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "mission user unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "mission user unavailable")
		return
	}
	if auth.IsTemporarilyDisabledUser(ids[0], user.Email) {
		writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
		return
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: user.ID, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "not a workspace member")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "membership unavailable")
		return
	}
	scope := auth.MissionTaskReadScope
	if member.Role == "owner" {
		scope = auth.MissionOwnerReadScope
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.MissionReadClaimsWithScope(uuidToString(user.ID), user.Email, uuidToString(ws), scope, time.Now()))
	signed, err := token.SignedString(auth.JWTSecret())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token exchange failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"access_token": signed, "expires_in": int(auth.MissionReadTTL.Seconds()), "workspace_id": uuidToString(ws), "user_id": uuidToString(user.ID), "role": member.Role, "scope": scope})
}
