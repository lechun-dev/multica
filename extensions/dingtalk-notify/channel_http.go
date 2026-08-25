package notify

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ChannelHTTPHandler exposes the Agent Bot/channel settings surface. The host
// still owns authentication, agent existence checks, and route mounting.
type ChannelHTTPHandler struct{ Service AgentChannelService }

func (h ChannelHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	identity, ok := AuthenticatedIdentityFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "authenticated workspace member is required")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/dingtalk/agents/"), "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "channels" {
		http.NotFound(w, r)
		return
	}
	agentID := parts[0]
	requester := ChannelRequester{WorkspaceID: identity.WorkspaceID, MemberID: identity.MemberID, IsAdmin: identity.IsAdmin}
	switch {
	case r.Method == http.MethodGet && len(parts) == 2:
		channels, err := h.Service.List(r.Context(), requester, agentID)
		if err != nil {
			writeJSONError(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
	case r.Method == http.MethodPut && len(parts) == 2:
		var channel AgentChannel
		if err := json.NewDecoder(r.Body).Decode(&channel); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid channel JSON")
			return
		}
		channel.WorkspaceID, channel.AgentID = identity.WorkspaceID, agentID
		if err := h.Service.Upsert(r.Context(), requester, channel); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
	case r.Method == http.MethodDelete && len(parts) == 3:
		if err := h.Service.Deactivate(r.Context(), requester, agentID, parts[2]); err != nil {
			writeJSONError(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deactivated": true})
	default:
		http.NotFound(w, r)
	}
}
