package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDingTalkProviderRefreshesTokenAfterUnauthorized(t *testing.T) {
	var tokenCalls, sendCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			atomic.AddInt32(&tokenCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expireIn": 7200})
		case "/v1.0/robot/oToMessages/batchSend":
			if atomic.AddInt32(&sendCalls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":"InvalidAuthentication"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &DingTalkProvider{BaseURL: srv.URL, ClientID: "app", ClientSecret: "secret", RobotCode: "robot"}
	err := p.Send(context.Background(), Message{EventID: "e1", WorkspaceID: "w1", ChannelType: "p2p", DingUserID: "user", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 2 || sendCalls != 2 {
		t.Fatalf("token calls=%d send calls=%d", tokenCalls, sendCalls)
	}
}

func TestDingTalkProviderClassifiesRateLimitAsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "accessToken") {
			_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expireIn": 7200})
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := &DingTalkProvider{BaseURL: srv.URL, ClientID: "app", ClientSecret: "secret", RobotCode: "robot"}
	err := p.Send(context.Background(), Message{ChannelType: "group", ChannelID: "group", Text: "hello"})
	var retry RetryableError
	if !errors.As(err, &retry) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestDingTalkOAuthProviderExchangesCodeAndLoadsIdentity(t *testing.T) {
	client := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/oauth-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"oauth-token"}`), nil
		case "/me":
			return jsonResponse(http.StatusOK, `{"unionId":"union-1","openId":"open-1","nick":"Alice"}`), nil
		case "/app-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"app-token"}`), nil
		case "/union":
			if req.URL.Query().Get("access_token") != "app-token" {
				return jsonResponse(http.StatusUnauthorized, `{}`), nil
			}
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1"}}`), nil
		case "/detail":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1","unionid":"union-1","name":"Alice","email":"Alice@Example.com","avatar":"https://example.test/alice.png"}}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	p := DingTalkOAuthProvider{
		Client:         client,
		AuthURL:        "https://example.test/auth",
		TokenURL:       "https://example.test/oauth-token",
		UserURL:        "https://example.test/me",
		AppTokenURL:    "https://example.test/app-token",
		UnionLookupURL: "https://example.test/union",
		UserDetailURL:  "https://example.test/detail",
		ClientID:       "id",
		ClientSecret:   "secret",
	}
	url, err := p.AuthorizationURL(context.Background(), "state", "https://app/callback")
	if err != nil || !strings.Contains(url, "state=state") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	user, err := p.ExchangeCode(context.Background(), "code", "https://app/callback")
	if err != nil || user.DingUserID != "u1" || user.UnionID != "union-1" || user.Email != "alice@example.com" || user.AvatarURL != "https://example.test/alice.png" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
}

func TestDingTalkOAuthProviderBackfillsMissingNickname(t *testing.T) {
	client := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/oauth-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"oauth-token"}`), nil
		case "/me":
			return jsonResponse(http.StatusOK, `{"userId":"u1","unionId":"union-1","openId":"open-1","email":"alice@example.com","avatarUrl":"https://example.test/alice.png"}`), nil
		case "/app-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"app-token"}`), nil
		case "/union":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1"}}`), nil
		case "/detail":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1","name":"Alice"}}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	p := DingTalkOAuthProvider{
		Client:         client,
		TokenURL:       "https://example.test/oauth-token",
		UserURL:        "https://example.test/me",
		AppTokenURL:    "https://example.test/app-token",
		UnionLookupURL: "https://example.test/union",
		UserDetailURL:  "https://example.test/detail",
		ClientID:       "id",
		ClientSecret:   "secret",
	}

	user, err := p.ExchangeCode(context.Background(), "code", "https://app/callback")
	if err != nil {
		t.Fatal(err)
	}
	if user.Name != "Alice" || user.AvatarURL != "https://example.test/alice.png" {
		t.Fatalf("user=%+v", user)
	}
}

func TestDingTalkOAuthProviderLoadsMultipleDepartmentsWithoutExposingIDsAsNames(t *testing.T) {
	client := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/oauth-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"oauth-token"}`), nil
		case "/me":
			return jsonResponse(http.StatusOK, `{"userId":"oauth-user","unionId":"union-1","openId":"open-1","nick":"Alice","email":"alice@example.com","avatarUrl":"https://example.test/alice.png"}`), nil
		case "/app-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"app-token"}`), nil
		case "/union":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1"}}`), nil
		case "/detail":
			var payload struct {
				UserID string `json:"userid"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				return nil, err
			}
			if payload.UserID != "u1" {
				return jsonResponse(http.StatusBadRequest, `{}`), nil
			}
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1","name":"Alice","dept_id_list":[42,"43",42]}}`), nil
		case "/department":
			var payload struct {
				DepartmentID json.RawMessage `json:"dept_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				return nil, err
			}
			switch strings.Trim(string(payload.DepartmentID), `"`) {
			case "42":
				return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"name":"Engineering"}}`), nil
			case "43":
				return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"name":"Product"}}`), nil
			default:
				return jsonResponse(http.StatusBadRequest, `{}`), nil
			}
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	p := DingTalkOAuthProvider{
		Client:              client,
		TokenURL:            "https://example.test/oauth-token",
		UserURL:             "https://example.test/me",
		AppTokenURL:         "https://example.test/app-token",
		UnionLookupURL:      "https://example.test/union",
		UserDetailURL:       "https://example.test/detail",
		DepartmentDetailURL: "https://example.test/department",
		ClientID:            "id",
		ClientSecret:        "secret",
	}

	user, err := p.ExchangeCode(context.Background(), "code", "https://app/callback")
	if err != nil {
		t.Fatal(err)
	}
	if !user.DepartmentsSynced {
		t.Fatal("departments were not marked as synchronized")
	}
	want := []DingTalkDepartment{{ID: "42", Name: "Engineering"}, {ID: "43", Name: "Product"}}
	if len(user.Departments) != len(want) {
		t.Fatalf("departments=%+v, want %+v", user.Departments, want)
	}
	for i := range want {
		if user.Departments[i] != want[i] {
			t.Fatalf("departments[%d]=%+v, want %+v", i, user.Departments[i], want[i])
		}
	}
}

func TestDingTalkOAuthProviderDoesNotFailLoginWhenDepartmentLookupFails(t *testing.T) {
	client := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/oauth-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"oauth-token"}`), nil
		case "/me":
			return jsonResponse(http.StatusOK, `{"userId":"u1","unionId":"union-1","openId":"open-1","nick":"Alice","email":"alice@example.com","avatarUrl":"https://example.test/alice.png"}`), nil
		case "/app-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"app-token"}`), nil
		case "/detail":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1","name":"Alice","dept_id_list":[42]}}`), nil
		case "/department":
			return jsonResponse(http.StatusOK, `{"errcode":60011,"errmsg":"permission denied"}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	p := DingTalkOAuthProvider{
		Client:              client,
		TokenURL:            "https://example.test/oauth-token",
		UserURL:             "https://example.test/me",
		AppTokenURL:         "https://example.test/app-token",
		UserDetailURL:       "https://example.test/detail",
		DepartmentDetailURL: "https://example.test/department",
		ClientID:            "id",
		ClientSecret:        "secret",
	}

	user, err := p.ExchangeCode(context.Background(), "code", "https://app/callback")
	if err != nil {
		t.Fatal(err)
	}
	if user.DepartmentsSynced || len(user.Departments) != 0 {
		t.Fatalf("unexpected department profile: %+v", user)
	}
}

func TestDingTalkOAuthProviderUsesEnterpriseScopeWhenCorpIDIsConfigured(t *testing.T) {
	p := DingTalkOAuthProvider{ClientID: "client", CorpID: "corp"}
	got, err := p.AuthorizationURL(context.Background(), "state", "https://example.test/callback")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("scope") != "openid corpid" {
		t.Fatalf("scope=%q", parsed.Query().Get("scope"))
	}
	if parsed.Query().Get("corpId") != "corp" {
		t.Fatalf("corpId=%q", parsed.Query().Get("corpId"))
	}
	if parsed.Query().Get("prompt") != "consent" {
		t.Fatalf("prompt=%q", parsed.Query().Get("prompt"))
	}
}

func TestDingTalkOAuthProviderSurfacesJSONErrorEnvelope(t *testing.T) {
	client := httpDoerFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, `{"code":"Forbidden.AccessDenied","message":"permission denied"}`), nil
	})
	p := DingTalkOAuthProvider{Client: client, ClientID: "id", ClientSecret: "secret"}
	_, err := p.ExchangeCode(context.Background(), "code", "https://example.test/callback")
	var apiErr *DingTalkHTTPError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected DingTalkHTTPError, got %v", err)
	}
	if apiErr.Code != "Forbidden.AccessDenied" || apiErr.Message != "permission denied" {
		t.Fatalf("apiErr=%+v", apiErr)
	}
}

func TestDingTalkOAuthProviderSurfacesUserEndpointErrorEnvelope(t *testing.T) {
	client := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/oauth-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"oauth-token"}`), nil
		case "/me":
			return jsonResponse(http.StatusForbidden, `{"code":"Forbidden.AccessDenied","message":"directory permission denied"}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	p := DingTalkOAuthProvider{
		Client:       client,
		TokenURL:     "https://example.test/oauth-token",
		UserURL:      "https://example.test/me",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	_, err := p.ExchangeCode(context.Background(), "code", "https://app.example/callback")
	var apiErr *DingTalkHTTPError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected DingTalkHTTPError, got %v", err)
	}
	if apiErr.Path != "https://example.test/me" || apiErr.Code != "Forbidden.AccessDenied" || apiErr.Message != "directory permission denied" {
		t.Fatalf("apiErr=%+v", apiErr)
	}
}

func TestDingTalkOAuthProviderPreservesOAuthEmailWhenEnterpriseEmailIsEmpty(t *testing.T) {
	client := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/oauth-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"oauth-token"}`), nil
		case "/me":
			return jsonResponse(http.StatusOK, `{"unionId":"union-1","openId":"open-1","nick":"Alice","email":"alice@example.com"}`), nil
		case "/app-token":
			return jsonResponse(http.StatusOK, `{"accessToken":"app-token"}`), nil
		case "/union":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1"}}`), nil
		case "/detail":
			return jsonResponse(http.StatusOK, `{"errcode":0,"result":{"userid":"u1","unionid":"union-1","name":"Alice","email":""}}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	p := DingTalkOAuthProvider{
		Client:         client,
		TokenURL:       "https://example.test/oauth-token",
		UserURL:        "https://example.test/me",
		AppTokenURL:    "https://example.test/app-token",
		UnionLookupURL: "https://example.test/union",
		UserDetailURL:  "https://example.test/detail",
		ClientID:       "id",
		ClientSecret:   "secret",
	}

	user, err := p.ExchangeCode(context.Background(), "code", "https://app/callback")
	if err != nil {
		t.Fatal(err)
	}
	if user.DingUserID != "u1" || user.Email != "alice@example.com" {
		t.Fatalf("user=%+v", user)
	}
}
