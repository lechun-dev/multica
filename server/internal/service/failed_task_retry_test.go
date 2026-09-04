package service

import (
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMatchesRetryPolicy(t *testing.T) {
	tests := []struct {
		name      string
		policy    db.TaskRetryPolicy
		reason    string
		errorText string
		want      bool
	}{
		{
			name:   "failure reason",
			policy: db.TaskRetryPolicy{MatchType: "failure_reason", MatchValue: "timeout"},
			reason: "timeout",
			want:   true,
		},
		{
			name:      "http status",
			policy:    db.TaskRetryPolicy{MatchType: "http_status", MatchValue: "400"},
			errorText: "provider returned HTTP 400 while creating a response",
			want:      true,
		},
		{
			name:      "error contains case insensitive",
			policy:    db.TaskRetryPolicy{MatchType: "error_contains", MatchValue: "connection closed"},
			errorText: "API Error: CONNECTION CLOSED mid-response",
			want:      true,
		},
		{
			name:      "status does not match a substring",
			policy:    db.TaskRetryPolicy{MatchType: "http_status", MatchValue: "400"},
			errorText: "request id 1400 was not accepted",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesRetryPolicy(tt.policy, tt.reason, tt.errorText); got != tt.want {
				t.Fatalf("matchesRetryPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryPolicyDelay(t *testing.T) {
	if got := retryPolicyDelay([]byte(`[0, 5, 30]`), 2); got != 5*time.Second {
		t.Fatalf("second failed attempt delay = %s, want 5s", got)
	}
	if got := retryPolicyDelay([]byte(`[0, 5, 30]`), 9); got != 30*time.Second {
		t.Fatalf("delay after schedule end = %s, want 30s", got)
	}
	if got := retryPolicyDelay([]byte(`not-json`), 1); got != 0 {
		t.Fatalf("invalid schedule delay = %s, want 0", got)
	}
}
