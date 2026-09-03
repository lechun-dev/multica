package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type identityContextKey struct{}

type AuthenticatedIdentity struct {
	WorkspaceID string
	MemberID    string
	IsAdmin     bool
}

func WithAuthenticatedIdentity(ctx context.Context, identity AuthenticatedIdentity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func AuthenticatedIdentityFromContext(ctx context.Context) (AuthenticatedIdentity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(AuthenticatedIdentity)
	return identity, ok && identity.WorkspaceID != "" && identity.MemberID != ""
}

// OAuthHTTPHandler provides host-neutral endpoints. Authentication and
// workspace selection remain the host's responsibility and are injected into
// the request context with WithAuthenticatedIdentity.
type OAuthHTTPHandler struct {
	Service      *OAuthService
	RedirectURI  string
	TestProvider Provider
}

func (h OAuthHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	identity, ok := AuthenticatedIdentityFromContext(r.Context())
	isCallback := r.Method == http.MethodGet && r.URL.Path == "/dingtalk/oauth/callback"
	if !ok && !isCallback {
		writeJSONError(w, http.StatusUnauthorized, "authenticated workspace member is required")
		return
	}
	if h.Service == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "DingTalk OAuth is not configured")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/dingtalk/oauth/start":
		auth, err := h.Service.Begin(r.Context(), identity.WorkspaceID, identity.MemberID, h.RedirectURI)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"authorization_url": auth.URL, "state": auth.State})
	case r.Method == http.MethodGet && r.URL.Path == "/dingtalk/oauth/callback":
		binding, err := h.Service.Complete(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"), h.RedirectURI)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"active": binding.Active, "ding_user_id": binding.DingUserID, "union_id": binding.UnionID, "open_id": binding.OpenID})
	case r.Method == http.MethodGet && r.URL.Path == "/dingtalk/binding":
		binding, found, err := h.Service.Bindings.Get(r.Context(), identity.WorkspaceID, identity.MemberID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, map[string]any{"bound": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"bound": binding.Active, "active": binding.Active, "ding_user_id": binding.DingUserID, "union_id": binding.UnionID, "open_id": binding.OpenID})
	case r.Method == http.MethodPost && r.URL.Path == "/dingtalk/binding/revoke":
		if err := h.Service.Bindings.Revoke(r.Context(), identity.WorkspaceID, identity.MemberID, time.Now()); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
	case r.Method == http.MethodPost && r.URL.Path == "/dingtalk/binding/test":
		if h.TestProvider == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "DingTalk provider is not configured")
			return
		}
		binding, found, err := h.Service.Bindings.Get(r.Context(), identity.WorkspaceID, identity.MemberID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found || !binding.Active || binding.DingUserID == "" {
			writeJSONError(w, http.StatusBadRequest, "DingTalk account is not actively bound")
			return
		}
		message := Message{EventID: "test-" + identity.MemberID + "-" + time.Now().UTC().Format("20060102150405.000000000"), WorkspaceID: identity.WorkspaceID, TargetID: identity.MemberID, TargetKind: "member", DingUserID: binding.DingUserID, ChannelType: "p2p", Text: "MissionOS 钉钉通知测试消息"}
		if err := h.TestProvider.Send(r.Context(), message); err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(message)})
}
