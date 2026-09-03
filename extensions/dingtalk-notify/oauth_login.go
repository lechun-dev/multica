package notify

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrInvalidLoginState = errors.New("oauth state is invalid or expired")

type LoginClient string

const (
	LoginClientWeb                  LoginClient = "web"
	LoginClientDesktop              LoginClient = "desktop"
	LoginClientDesktopDev           LoginClient = "desktop-dev"
	LoginClientDesktopLechun        LoginClient = "desktop-lechun"
	LoginClientDesktopLechunPreview LoginClient = "desktop-lechun-preview"
)

func ParseLoginClient(value string) (LoginClient, error) {
	switch LoginClient(strings.TrimSpace(value)) {
	case "", LoginClientWeb:
		return LoginClientWeb, nil
	case LoginClientDesktop:
		return LoginClientDesktop, nil
	case LoginClientDesktopDev:
		return LoginClientDesktopDev, nil
	case LoginClientDesktopLechun:
		return LoginClientDesktopLechun, nil
	case LoginClientDesktopLechunPreview:
		return LoginClientDesktopLechunPreview, nil
	default:
		return "", errors.New("unsupported OAuth client")
	}
}

// LoginClientFromState extracts the client marker from a state that has
// already been verified and consumed by Complete. The random prefix never
// contains a dot because randomState uses base64url without padding.
func LoginClientFromState(state string) LoginClient {
	if idx := strings.Index(state, ".next."); idx >= 0 {
		state = state[:idx]
	}
	if strings.HasSuffix(state, "."+string(LoginClientDesktopDev)) {
		return LoginClientDesktopDev
	}
	if strings.HasSuffix(state, "."+string(LoginClientDesktopLechun)) {
		return LoginClientDesktopLechun
	}
	if strings.HasSuffix(state, "."+string(LoginClientDesktopLechunPreview)) {
		return LoginClientDesktopLechunPreview
	}
	if strings.HasSuffix(state, "."+string(LoginClientDesktop)) {
		return LoginClientDesktop
	}
	return LoginClientWeb
}

// LoginOAuthService is the unauthenticated half of the DingTalk OAuth flow.
// It deliberately stops at a verified DingTalk identity. The host supplies
// the account lookup/creation and session issuance adapter, so this module
// never duplicates Multica's user or JWT rules.
type LoginOAuthService struct {
	Provider OAuthProvider
	Store    LoginStateStore
	Now      func() time.Time
	TTL      time.Duration
	mu       sync.Mutex
	pendings map[string]time.Time
}

// LoginStateStore lets the host persist OAuth state across replicas/restarts.
// A nil store keeps the in-memory implementation for local/mock mode.
type LoginStateStore interface {
	Put(ctx context.Context, state string, expiresAt time.Time) error
	Consume(ctx context.Context, state string, now time.Time) (bool, error)
}

// Begin creates a short-lived, single-use state for a public login redirect.
func (s *LoginOAuthService) Begin(ctx context.Context, redirectURI string) (Authorization, error) {
	return s.BeginForClientWithNext(ctx, redirectURI, LoginClientWeb, "")
}

// BeginForClient carries a trusted client marker through DingTalk's OAuth
// round trip. The full state remains random, server-stored, and single-use.
func (s *LoginOAuthService) BeginForClient(ctx context.Context, redirectURI string, client LoginClient) (Authorization, error) {
	return s.BeginForClientWithNext(ctx, redirectURI, client, "")
}

// BeginForClientWithNext carries a validated, same-origin post-login path
// through the one-time OAuth state. Keeping it in state avoids a schema change
// while still working across replicas and server restarts.
func (s *LoginOAuthService) BeginForClientWithNext(ctx context.Context, redirectURI string, client LoginClient, next string) (Authorization, error) {
	if s.Provider == nil {
		return Authorization{}, errors.New("oauth provider is required")
	}
	if redirectURI == "" {
		return Authorization{}, errors.New("redirect URI is required")
	}
	parsedClient, err := ParseLoginClient(string(client))
	if err != nil {
		return Authorization{}, err
	}
	state, err := randomState()
	if err != nil {
		return Authorization{}, err
	}
	state += "." + string(parsedClient)
	if next != "" {
		state += ".next." + base64.RawURLEncoding.EncodeToString([]byte(next))
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	s.mu.Lock()
	if s.pendings == nil {
		s.pendings = make(map[string]time.Time)
	}
	s.pendings[state] = now.Add(ttl)
	s.mu.Unlock()
	if s.Store != nil {
		if err := s.Store.Put(ctx, state, now.Add(ttl)); err != nil {
			s.mu.Lock()
			delete(s.pendings, state)
			s.mu.Unlock()
			return Authorization{}, err
		}
	}
	url, err := s.Provider.AuthorizationURL(ctx, state, redirectURI)
	if err != nil {
		s.mu.Lock()
		delete(s.pendings, state)
		s.mu.Unlock()
		return Authorization{}, err
	}
	return Authorization{URL: url, State: state}, nil
}

// LoginNextFromState returns the optional post-login path carried by a
// verified DingTalk OAuth state. Invalid or absent values are ignored.
func LoginNextFromState(state string) string {
	marker := ".next."
	idx := strings.LastIndex(state, marker)
	if idx < 0 || idx+len(marker) == len(state) {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(state[idx+len(marker):])
	if err != nil {
		return ""
	}
	return string(raw)
}

// Complete verifies and consumes a login state before exchanging the code.
// The returned identity is not yet a Multica session; the host must resolve it
// using a trusted union/open ID or email policy.
func (s *LoginOAuthService) Complete(ctx context.Context, state, code, redirectURI string) (OAuthUser, error) {
	if s.Provider == nil {
		return OAuthUser{}, errors.New("oauth provider is required")
	}
	if state == "" || code == "" || redirectURI == "" {
		return OAuthUser{}, errors.New("oauth state, code and redirect URI are required")
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	var ok bool
	if s.Store != nil {
		var err error
		ok, err = s.Store.Consume(ctx, state, now)
		if err != nil {
			return OAuthUser{}, err
		}
		s.mu.Lock()
		delete(s.pendings, state)
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		expiresAt, found := s.pendings[state]
		if found {
			delete(s.pendings, state)
		}
		s.mu.Unlock()
		ok = found && !expiresAt.Before(now)
	}
	if !ok {
		return OAuthUser{}, ErrInvalidLoginState
	}
	user, err := s.Provider.ExchangeCode(ctx, code, redirectURI)
	if err != nil {
		return OAuthUser{}, fmt.Errorf("exchange DingTalk OAuth code: %w", err)
	}
	if user.DingUserID == "" && user.UnionID == "" && user.OpenID == "" {
		return OAuthUser{}, errors.New("oauth provider returned no stable DingTalk identity")
	}
	return user, nil
}
