package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/redis/go-redis/v9"
)

const missionTestWorkspace = "11111111-1111-4111-8111-111111111111"

func TestMissionReadTokenScope(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		mutate             func(*http.Request, jwt.MapClaims)
		status             int
	}{
		{"list", "GET", "/api/issues?workspace_id=" + missionTestWorkspace, nil, 200},
		{"detail", "GET", "/api/issues/HAN-1", nil, 200},
		{"uuid", "GET", "/api/issues/" + missionTestWorkspace, nil, 200},
		{"agent list", "GET", "/internal/agent/issues?workspace_id=" + missionTestWorkspace, nil, 200},
		{"agent detail", "GET", "/internal/agent/issues/HAN-1", nil, 200},
		{"agent search", "GET", "/internal/agent/issues/search", nil, 403},
		{"agent children", "GET", "/internal/agent/issues/HAN-1/children", nil, 403},
		{"write", "PATCH", "/api/issues/HAN-1", nil, 403},
		{"refresh", "POST", "/api/auth/refresh", nil, 403},
		{"me", "GET", "/api/me", nil, 403},
		{"search", "GET", "/api/issues/search", nil, 403},
		{"children", "GET", "/api/issues/HAN-1/children", nil, 403},
		{"encoded slash", "GET", "/api/issues/HAN-1%2Fcomments", nil, 403},
		{"missing workspace", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { r.Header.Del("X-Workspace-ID") }, 403},
		{"workspace mismatch", "GET", "/api/issues?workspace_id=22222222-2222-4222-8222-222222222222", nil, 403},
		{"duplicate selector", "GET", "/api/issues?workspace_id=" + missionTestWorkspace + "&workspace_id=bad", nil, 403},
		{"slug", "GET", "/api/issues?workspace_slug=other", nil, 403},
		{"slug header", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { r.Header.Set("X-Workspace-Slug", "other") }, 403},
		{"unknown scope", "GET", "/internal/agent/issues", func(r *http.Request, c jwt.MapClaims) { c["scope"] = "agent_admin" }, 403},
		{"issuer", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { c["iss"] = "other" }, 403},
		{"audience", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { c["aud"] = "other" }, 403},
		{"long expiry", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { c["exp"] = time.Now().Add(time.Hour).Unix() }, 403},
		{"future issued", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Minute).Unix() }, 403},
		{"expired", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Second).Unix() }, 401},
		{"cookie", "GET", "/api/issues", func(r *http.Request, c jwt.MapClaims) { r.Header.Set("X-Test-Cookie", "true") }, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-Workspace-ID", missionTestWorkspace)
			claims := auth.MissionReadClaims("33333333-3333-4333-8333-333333333333", "mission-test@example.com", missionTestWorkspace, time.Now())
			if tc.mutate != nil {
				tc.mutate(req, claims)
			}
			token := generateToken(claims, auth.JWTSecret())
			if req.Header.Get("X-Test-Cookie") != "" {
				req.AddCookie(&http.Cookie{Name: auth.AuthCookieName, Value: token})
			} else {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-User-ID") != claims["sub"] {
					t.Fatal("wrong acting user")
				}
				w.WriteHeader(200)
			})).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("read token renewed a browser session")
			}
		})
	}
}

func TestMissionReadTokenRejectedByDaemon(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/daemon/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer "+generateToken(auth.MissionReadClaims("user", "mission-test@example.com", missionTestWorkspace, time.Now()), auth.JWTSecret()))
	w := httptest.NewRecorder()
	DaemonAuth(nil, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("daemon accepted task-read token") })).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}

type missionNonceStore struct {
	redis.UniversalClient
	seen map[string]bool
	err  error
}

func (s *missionNonceStore) SetNX(ctx context.Context, key string, value any, ttl time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(ctx)
	if s.err != nil {
		cmd.SetErr(s.err)
		return cmd
	}
	cmd.SetVal(!s.seen[key])
	s.seen[key] = true
	return cmd
}

func missionSignedRequest(secret, body string, now time.Time) *http.Request {
	r := httptest.NewRequest("POST", "/internal/auth/dingtalk/exchange", strings.NewReader(body))
	timestamp, nonce := strconv.FormatInt(now.Unix(), 10), strings.Repeat("a", 32)
	r.Header.Set("X-Mission-Timestamp", timestamp)
	r.Header.Set("X-Mission-Nonce", nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("POST\n/internal/auth/dingtalk/exchange\n" + timestamp + "\n" + nonce + "\n" + body))
	r.Header.Set("X-Mission-Signature", hex.EncodeToString(mac.Sum(nil)))
	return r
}

func TestMissionExchangeSignatureAndReplay(t *testing.T) {
	secret := strings.Repeat("s", 32)
	body := `{"ding_user_id":"trusted","workspace_id":"` + missionTestWorkspace + `"}`
	store := &missionNonceStore{seen: map[string]bool{}}
	h := MissionExchangeAuth(secret, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, want := range []int{204, 401} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, missionSignedRequest(secret, body, time.Now()))
		if w.Code != want {
			t.Fatalf("got %d, want %d", w.Code, want)
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Method = "GET" },
		func(r *http.Request) { r.URL.RawQuery = "other=1" },
		func(r *http.Request) { r.Header.Set("X-Mission-Nonce", "bad") },
		func(r *http.Request) { r.Header.Set("X-Mission-Signature", strings.Repeat("0", 64)) },
	} {
		r := missionSignedRequest(secret, body, time.Now())
		mutate(r)
		if validMissionSignature(secret, r, []byte(body), time.Now()) {
			t.Fatal("accepted tampered request")
		}
	}
	r := missionSignedRequest(secret, body, time.Now())
	if validMissionSignature(secret, r, []byte(body+" "), time.Now()) {
		t.Fatal("accepted changed body")
	}
	if validMissionSignature(secret, r, []byte(body), time.Now().Add(61*time.Second)) {
		t.Fatal("accepted stale timestamp")
	}
	for _, store := range []redis.UniversalClient{nil, &missionNonceStore{err: errors.New("offline")}} {
		w := httptest.NewRecorder()
		MissionExchangeAuth(secret, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("failed open") })).ServeHTTP(w, missionSignedRequest(secret, body, time.Now()))
		if w.Code != 503 {
			t.Fatalf("got %d, want 503", w.Code)
		}
	}
}
