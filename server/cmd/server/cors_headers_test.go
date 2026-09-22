package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The app advertises its capabilities on the cancel request (#5219). Browsers
// preflight a custom request header, so an entry missing from AllowedHeaders is
// not a degraded feature — it is a failed request: the cancel never reaches the
// server, and the user's prompt is lost in a way no server-side test can see.
func TestCORSAllowedHeaders_IncludeClientCapabilities(t *testing.T) {
	if !slices.Contains(corsAllowedHeaders, "X-Client-Capabilities") {
		t.Fatalf("X-Client-Capabilities missing from CORS allowed headers: %v", corsAllowedHeaders)
	}
	// Named so the constant and the header travel together: the capability is
	// useless if the header carrying it cannot cross the preflight.
	if protocol.AppCapabilityChatDraftRestoreV1 == "" {
		t.Fatal("AppCapabilityChatDraftRestoreV1 must be a non-empty capability token")
	}
}

// Workspace subscription checkout and portal requests use Idempotency-Key to
// make retries safe. Browsers preflight that custom request header, so omitting
// it from AllowedHeaders prevents the billing request from reaching the server.
func TestCORSAllowedHeaders_IncludeIdempotencyKey(t *testing.T) {
	if !slices.Contains(corsAllowedHeaders, "Idempotency-Key") {
		t.Fatalf("Idempotency-Key missing from CORS allowed headers: %v", corsAllowedHeaders)
	}
}

// Timeline and comment-list endpoints report defensive hard-cap clamps with
// custom response headers.
// Custom response headers are not readable from browser JS unless the server
// exposes them, and only the CORS-safelisted headers are exposed by default — so
// an entry missing here is not a degraded signal, it is no signal at all: the
// header arrives on the wire and the client cannot see it (MUL-5492).
func TestCORSExposedHeaders_IncludeTruncationSignals(t *testing.T) {
	for _, want := range []string{
		handler.HeaderCommentsTruncated,
		handler.HeaderTimelineTruncated,
	} {
		if !slices.Contains(corsExposedHeaders, want) {
			t.Errorf("%s missing from CORS exposed headers: %v", want, corsExposedHeaders)
		}
	}
}

func headerListContains(header, want string) bool {
	for _, value := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// Every header the browser clients send has to be preflight-allowed, and the
// CSRF header is the one where getting this wrong is invisible in same-origin
// development and fatal in a split app/API deployment: the preflight fails, so
// the request never reaches a handler and the failure looks nothing like a
// rejected CSRF token.
//
// It is also the header whose name must not change casually. A rolled-back
// server allowlists only the names it shipped with, so a client that starts
// sending a NEW header can be blocked by a version of the server that predates
// it — which is why MUL-7436 carries two CSRF cookie values through this one
// header name rather than adding a second.
func TestRouterCORSAllowsTheCSRFHeader(t *testing.T) {
	const origin = "https://cors-client.example"
	t.Setenv("CORS_ALLOWED_ORIGINS", origin)
	router := NewRouter(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil)

	req := httptest.NewRequest(http.MethodOptions, "/api/config", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", auth.CSRFHeaderName)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusOK)
	}
	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	if !headerListContains(allowed, auth.CSRFHeaderName) {
		t.Errorf("Access-Control-Allow-Headers = %q, missing %q", allowed, auth.CSRFHeaderName)
	}
}

// Pins the invariant rather than one name: whatever header the auth package
// tells clients to send, the router must allow. If someone introduces a second
// CSRF header without touching corsAllowedHeaders, this fails here instead of
// in a production split-origin deployment.
func TestCSRFHeaderIsInTheCORSAllowlist(t *testing.T) {
	for _, h := range corsAllowedHeaders {
		if strings.EqualFold(h, auth.CSRFHeaderName) {
			return
		}
	}
	t.Fatalf("auth.CSRFHeaderName (%q) is not in corsAllowedHeaders; browsers would fail the preflight", auth.CSRFHeaderName)
}
