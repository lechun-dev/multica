package notify

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type DeliveryHTTPHandler struct {
	Store Store
	Read  OutboxReader
}

func (h DeliveryHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	identity, ok := AuthenticatedIdentityFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authenticated workspace member is required")
		return
	}
	if h.Store == nil || h.Read == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "delivery store is not configured")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dingtalk/deliveries"), "/")
	if r.Method == http.MethodGet && path == "" {
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		items, err := h.Read.List(r.Context(), identity.WorkspaceID, limit)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		views := make([]map[string]any, 0, len(items))
		for _, item := range items {
			views = append(views, deliveryView(item))
		}
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": views})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/retry") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/retry")
		items, err := h.Read.List(r.Context(), identity.WorkspaceID, 200)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, item := range items {
			if item.ID == id || item.IdempotencyKey == id {
				if err := h.Store.MarkRetry(r.Context(), item.ID, time.Now(), item.Attempts, "manual retry"); err != nil {
					writeJSONError(w, http.StatusInternalServerError, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]bool{"queued": true})
				return
			}
		}
		writeJSONError(w, http.StatusNotFound, "delivery not found")
		return
	}
	http.NotFound(w, r)
}

func deliveryView(item OutboxItem) map[string]any {
	return map[string]any{"id": item.ID, "event_id": item.Message.EventID, "target_id": item.Message.TargetID, "target_kind": item.Message.TargetKind, "channel_id": item.Message.ChannelID, "channel_type": item.Message.ChannelType, "status": item.Status, "attempts": item.Attempts, "last_error": item.LastError, "next_attempt_at": item.NextAttemptAt, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt}
}
