package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	notify "github.com/lechun-dev/multica/extensions/dingtalk-notify"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// dingtalkLoginHandler is the thin host adapter for the independently-owned
// DingTalk extension. The extension verifies OAuth state and returns a
// provider identity; this adapter alone applies Multica's existing account
// lookup, JWT, and cookie rules.
type dingtalkLoginHandler struct {
	host        *handler.Handler
	pool        *pgxpool.Pool
	service     *notify.LoginOAuthService
	redirectURI string
	initErr     error
}

type dingtalkLoginRequest struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

func newDingTalkLoginHandler(host *handler.Handler, pool *pgxpool.Pool, redirectURI string) *dingtalkLoginHandler {
	clientID := strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_SECRET"))
	provider := notify.DingTalkOAuthProvider{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthURL:      strings.TrimSpace(os.Getenv("DINGTALK_OAUTH_AUTH_URL")),
		TokenURL:     strings.TrimSpace(os.Getenv("DINGTALK_OAUTH_TOKEN_URL")),
		UserURL:      strings.TrimSpace(os.Getenv("DINGTALK_OAUTH_USER_URL")),
		Scope:        strings.TrimSpace(os.Getenv("DINGTALK_OAUTH_SCOPE")),
		CorpID:       strings.TrimSpace(os.Getenv("DINGTALK_CORP_ID")),
	}
	h := &dingtalkLoginHandler{
		host:        host,
		pool:        pool,
		redirectURI: strings.TrimSpace(redirectURI),
		service:     &notify.LoginOAuthService{Provider: provider, Store: dingtalkOAuthStateStore{pool: pool}, TTL: 10 * time.Minute},
	}
	switch {
	case clientID == "" || clientSecret == "":
		h.initErr = errors.New("DingTalk OAuth client credentials are not configured")
	case pool == nil:
		h.initErr = errors.New("DingTalk OAuth database is unavailable")
	default:
		connConfig := *pool.Config().ConnConfig
		connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		schemaDB := stdlib.OpenDB(connConfig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		h.initErr = notify.EnsureSchema(ctx, schemaDB)
		cancel()
		if closeErr := schemaDB.Close(); h.initErr == nil && closeErr != nil {
			h.initErr = fmt.Errorf("close DingTalk schema database: %w", closeErr)
		}
	}
	if h.initErr != nil {
		slog.Warn("dingtalk login unavailable", "error", h.initErr)
	}
	return h
}

type dingtalkOAuthStateStore struct{ pool *pgxpool.Pool }

func (s dingtalkOAuthStateStore) Put(ctx context.Context, state string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dingtalk_notify_oauth_states (state, expires_at)
		VALUES ($1, $2)
		ON CONFLICT (state) DO UPDATE SET expires_at = EXCLUDED.expires_at`, state, expiresAt)
	return err
}

func (s dingtalkOAuthStateStore) Consume(ctx context.Context, state string, now time.Time) (bool, error) {
	result, err := s.pool.Exec(ctx, `
		DELETE FROM dingtalk_notify_oauth_states
		WHERE state = $1 AND expires_at > $2`, state, now)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func (h *dingtalkLoginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.host == nil || h.pool == nil || h.service == nil {
		writeDingTalkError(w, http.StatusServiceUnavailable, "DingTalk login is not configured")
		return
	}
	if h.redirectURI == "" {
		writeDingTalkError(w, http.StatusServiceUnavailable, "DingTalk OAuth redirect URI is not configured")
		return
	}
	if h.initErr != nil {
		writeDingTalkError(w, http.StatusServiceUnavailable, "DingTalk login is temporarily unavailable")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/auth/dingtalk/start":
		client, err := notify.ParseLoginClient(r.URL.Query().Get("client"))
		if err != nil {
			writeDingTalkError(w, http.StatusBadRequest, "unsupported DingTalk login client")
			return
		}
		authz, err := h.service.BeginForClient(r.Context(), h.redirectURI, client)
		if err != nil {
			slog.Warn("dingtalk login: start failed", "error", err)
			writeDingTalkError(w, http.StatusBadGateway, "Unable to start DingTalk login")
			return
		}
		http.Redirect(w, r, authz.URL, http.StatusFound)
	case r.Method == http.MethodPost && r.URL.Path == "/auth/dingtalk":
		h.complete(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *dingtalkLoginHandler) complete(w http.ResponseWriter, r *http.Request) {
	var req dingtalkLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDingTalkError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	identity, err := h.service.Complete(r.Context(), req.State, req.Code, h.redirectURI)
	if err != nil {
		if errors.Is(err, notify.ErrInvalidLoginState) {
			writeDingTalkError(w, http.StatusBadRequest, "DingTalk login state is invalid or expired")
			return
		}
		slog.Warn("dingtalk login: OAuth exchange failed", "error", err)
		writeDingTalkError(w, http.StatusBadGateway, "Unable to complete DingTalk login")
		return
	}
	user, err := h.resolveUser(r.Context(), identity)
	if err != nil {
		slog.Warn("dingtalk login: account resolution failed", "error", err)
		writeDingTalkError(w, http.StatusForbidden, "DingTalk account could not be linked to a Multica account")
		return
	}
	token, err := h.host.IssueLoginTokenForOAuth(user)
	if err != nil {
		writeDingTalkError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	if err := auth.SetAuthCookies(w, token); err != nil {
		slog.Warn("dingtalk login: failed to set auth cookies", "error", err)
	}
	if h.host.CFSigner != nil {
		for _, cookie := range h.host.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}
	writeJSON(w, http.StatusOK, handler.LoginResponse{Token: token, User: h.host.UserResponseForOAuth(user)})
}

func (h *dingtalkLoginHandler) resolveUser(ctx context.Context, identity notify.OAuthUser) (db.User, error) {
	var userID string
	err := h.pool.QueryRow(ctx, `
		SELECT multica_user_id
		FROM dingtalk_notify_identities
		WHERE active = true
		  AND (($1 <> '' AND ding_user_id = $1)
		    OR ($2 <> '' AND union_id = $2)
		    OR ($3 <> '' AND open_id = $3))
		LIMIT 1`, identity.DingUserID, identity.UnionID, identity.OpenID).Scan(&userID)
	if err == nil {
		userUUID, parseErr := util.ParseUUID(userID)
		if parseErr != nil {
			return db.User{}, fmt.Errorf("stored DingTalk identity has invalid Multica user id: %w", parseErr)
		}
		user, getErr := h.host.Queries.GetUser(ctx, userUUID)
		if getErr == nil {
			return user, nil
		}
		if !errors.Is(getErr, pgx.ErrNoRows) {
			return db.User{}, fmt.Errorf("load DingTalk-linked Multica user: %w", getErr)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, fmt.Errorf("lookup DingTalk identity: %w", err)
	}

	if strings.TrimSpace(identity.Email) == "" {
		return db.User{}, errors.New("DingTalk account has no trusted enterprise email")
	}
	user, _, err := h.host.FindOrCreateUserForOAuth(ctx, identity.Email)
	if err != nil {
		return db.User{}, fmt.Errorf("resolve Multica account: %w", err)
	}
	loginOnly := identity.DingUserID == ""
	_, err = h.pool.Exec(ctx, `
		INSERT INTO dingtalk_notify_identities
		    (ding_user_id, union_id, open_id, email, multica_user_id, active, login_only, updated_at)
		VALUES (NULLIF($1, ''), NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), $5, true, $6, now())
		ON CONFLICT (ding_user_id) DO UPDATE SET
		    union_id = COALESCE(EXCLUDED.union_id, dingtalk_notify_identities.union_id),
		    open_id = COALESCE(EXCLUDED.open_id, dingtalk_notify_identities.open_id),
		    email = COALESCE(EXCLUDED.email, dingtalk_notify_identities.email),
		    multica_user_id = EXCLUDED.multica_user_id,
		    active = true,
		    login_only = EXCLUDED.login_only,
		    updated_at = now()`, identity.DingUserID, identity.UnionID, identity.OpenID, strings.ToLower(strings.TrimSpace(identity.Email)), util.UUIDToString(user.ID), loginOnly)
	if err != nil {
		return db.User{}, fmt.Errorf("save DingTalk identity: %w", err)
	}
	return user, nil
}

func writeDingTalkError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(message)})
}
