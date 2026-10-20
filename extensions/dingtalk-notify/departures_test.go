package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func departureProvider(t *testing.T, serve http.HandlerFunc) DingTalkOAuthProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.Method != "POST" {
				t.Error("token must use POST")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["appKey"] != "test-app" || body["appSecret"] != "test-secret" {
				t.Error("missing application credentials")
			}
			_, _ = w.Write([]byte(`{"accessToken":"test-token"}`))
			return
		}
		if r.Method != "GET" || r.Header.Get("x-acs-dingtalk-access-token") != "test-token" || r.URL.Query().Has("access_token") {
			t.Error("departure lookup must use an application token header")
		}
		if r.URL.Query().Get("maxResults") != "50" || r.URL.Query().Get("startTime") == "" || r.URL.Query().Get("endTime") == "" {
			t.Error("missing range/pagination parameters")
		}
		serve(w, r)
	}))
	t.Cleanup(server.Close)
	return DingTalkOAuthProvider{ClientID: "test-app", ClientSecret: "test-secret", AppTokenURL: server.URL + "/token", DepartureListURL: server.URL + "/departures", DirectoryRequestInterval: -1, DirectoryRetryDelays: []time.Duration{0}}
}

func TestDepartureApplicationPagination(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(31 * 24 * time.Hour)
	calls := 0
	provider := departureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("startTime"))
		until, _ := time.Parse(time.RFC3339, r.URL.Query().Get("endTime"))
		if until.Sub(from) > 30*24*time.Hour {
			t.Error("range must be bounded")
		}
		record := `{"userId":"departed-1","leaveTime":"` + from.Format(time.RFC3339) + `"}`
		next := ""
		if calls == 1 {
			next = "page-2"
		}
		if calls == 2 && r.URL.Query().Get("nextToken") != "page-2" {
			t.Error("pagination lost cursor")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"records": json.RawMessage("[" + record + "]"), "nextToken": next})
	})
	ids, err := provider.LoadDepartedUserIDs(context.Background(), start, end)
	if err != nil || !reflect.DeepEqual(ids, []string{"departed-1"}) || calls != 3 {
		t.Fatalf("ids=%v calls=%d err=%v", ids, calls, err)
	}
}

func TestDepartureFailureNeverReturnsPartialIDs(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
	}{
		{"permission denied", `{"code":"Forbidden.AccessDenied","message":"permission required"}`, 403},
		{"legacy permission error", `{"errcode":60011,"errmsg":"permission required"}`, 200},
		{"missing records", `{}`, 200},
		{"null records", `{"records":null}`, 200},
		{"malformed JSON", `{"records":`, 200},
		{"missing identity", `{"records":[{"leaveTime":"2026-10-01T00:00:00Z"}]}`, 200},
		{"invalid date", `{"records":[{"userId":"x","leaveTime":"tomorrow"}]}`, 200},
		{"future departure", `{"records":[{"userId":"x","leaveTime":"2027-10-01T00:00:00Z"}]}`, 200},
		{"repeated cursor", `{"records":[],"nextToken":"same"}`, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			provider := departureProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					_, _ = w.Write([]byte(`{"records":[{"userId":"safe","leaveTime":"2026-10-01T00:00:00Z"}],"nextToken":"next"}`))
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			ids, err := provider.LoadDepartedUserIDs(context.Background(), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
			if err == nil || ids != nil {
				t.Fatalf("partial result accepted: %v %v", ids, err)
			}
			if strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "test-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestDepartureRateLimitAndEmptyResult(t *testing.T) {
	calls := 0
	provider := departureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"code":"Throttling","message":"retry"}`))
			return
		}
		_, _ = w.Write([]byte(`{"records":[]}`))
	})
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ids, err := provider.LoadDepartedUserIDs(context.Background(), start, start.Add(time.Hour))
	if err != nil || len(ids) != 0 || calls != 2 {
		t.Fatalf("ids=%v calls=%d err=%v", ids, calls, err)
	}
}
