package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 2026-10-09 coder(lq): Verify registered Prometheus output, including bounded
// labels, so request identities cannot create metric series or leak to scrapes.
func TestReadCacheMetricsRegisteredAndLabelsBounded(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	m := r.ReadCache
	m.RecordLookup("response", "table_rows", "hit")
	m.ObserveLoad("organization", "table_rows", "timeout", 250*time.Millisecond)
	m.RecordFallback("organization", "table_rows", "load_timeout")
	m.RecordInvalidation("response", "summary", "issue", "success")
	m.RecordLookup("permission", "effective_access", "hit")
	m.RecordFallback("permission", "issue_grants", "expired")
	m.RecordInvalidation("permission", "task", "mutation", "success")
	m.RecordInvalidation("permission", "project", "project", "success")
	for range 3 {
		m.RecordLookup("private-user", "/api/issues/private-id", "private-filter")
		m.ObserveLoad("private-user", "private-id", "private-filter", time.Millisecond)
		m.RecordFallback("private-user", "private-id", "private-filter")
		m.RecordInvalidation("private-user", "private-workspace", "private-filter", "private-id")
	}
	rec := httptest.NewRecorder()
	NewHandler(r.Gatherer).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`multica_read_cache_requests_total{cache="response",endpoint="table_rows",result="hit"} 1`,
		`multica_read_cache_requests_total{cache="other",endpoint="other",result="other"} 3`,
		`multica_read_cache_load_duration_seconds_count{cache="organization",endpoint="table_rows",result="timeout"} 1`,
		`multica_read_cache_load_duration_seconds_sum{cache="organization",endpoint="table_rows",result="timeout"} 0.25`,
		`multica_read_cache_fallbacks_total{cache="organization",endpoint="table_rows",reason="load_timeout"} 1`,
		`multica_read_cache_invalidations_total{cache="response",reason="issue",result="success",scope="summary"} 1`,
		`multica_read_cache_requests_total{cache="permission",endpoint="effective_access",result="hit"} 1`,
		`multica_read_cache_fallbacks_total{cache="permission",endpoint="issue_grants",reason="expired"} 1`,
		`multica_read_cache_invalidations_total{cache="permission",reason="mutation",result="success",scope="task"} 1`,
		`multica_read_cache_invalidations_total{cache="permission",reason="project",result="success",scope="project"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing metric %s", want)
		}
	}
	for _, value := range []string{"private-user", "private-id", "private-filter", "private-workspace"} {
		if strings.Contains(body, value) {
			t.Errorf("metric labels leaked %s", value)
		}
	}
	var disabled *ReadCacheMetrics
	disabled.RecordLookup("response", "table_rows", "hit")
	disabled.ObserveLoad("organization", "table_rows", "success", time.Millisecond)
	disabled.RecordFallback("response", "table_rows", "store_error")
	disabled.RecordInvalidation("response", "workspace", "issue", "success")
}
