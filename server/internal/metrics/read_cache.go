package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// 2026-10-09 coder(lq): Fixed labels show whether short read caches reduce SQL
// work or fall back under load, without exposing users, workspaces or filters.
type ReadCacheMetrics struct {
	Requests      *prometheus.CounterVec
	Loads         *prometheus.HistogramVec
	Fallbacks     *prometheus.CounterVec
	Invalidations *prometheus.CounterVec
}

func NewReadCacheMetrics() *ReadCacheMetrics {
	return &ReadCacheMetrics{
		Requests:      prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "multica", Subsystem: "read_cache", Name: "requests_total", Help: "Read cache lookups by cache, endpoint and result (hit, miss or bypass)."}, []string{"cache", "endpoint", "result"}),
		Loads:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "multica", Subsystem: "read_cache", Name: "load_duration_seconds", Help: "Actual response loads or organization database preloads, excluding singleflight followers.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}}, []string{"cache", "endpoint", "result"}),
		Fallbacks:     prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "multica", Subsystem: "read_cache", Name: "fallbacks_total", Help: "Reasons a cache read uses live SQL or cannot store a reusable response."}, []string{"cache", "endpoint", "reason"}),
		Invalidations: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "multica", Subsystem: "read_cache", Name: "invalidations_total", Help: "Cache generation writes by scope, reason and result; API mutations invalidate before and after."}, []string{"cache", "scope", "reason", "result"}),
	}
}

func boundedCacheLabel(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}
func cacheEndpointLabel(value string) string {
	return boundedCacheLabel(value, "issues", "query", "table_rows", "table_groups", "table_facets", "child_progress", "unread_count", "unread_summary", "effective_access", "access_control", "issue_grants", "project_grants", "project_roles", "task_roles")
}
func (m *ReadCacheMetrics) RecordLookup(cache, endpoint, result string) {
	if m != nil {
		m.Requests.WithLabelValues(boundedCacheLabel(cache, "response", "organization", "permission"), cacheEndpointLabel(endpoint), boundedCacheLabel(result, "hit", "miss", "bypass")).Inc()
	}
}
func (m *ReadCacheMetrics) ObserveLoad(cache, endpoint, result string, duration time.Duration) {
	if m != nil {
		m.Loads.WithLabelValues(boundedCacheLabel(cache, "response", "organization", "permission"), cacheEndpointLabel(endpoint), boundedCacheLabel(result, "success", "error", "timeout", "canceled")).Observe(duration.Seconds())
	}
}
func (m *ReadCacheMetrics) RecordFallback(cache, endpoint, reason string) {
	if m != nil {
		m.Fallbacks.WithLabelValues(boundedCacheLabel(cache, "response", "organization", "permission"), cacheEndpointLabel(endpoint), boundedCacheLabel(reason, "generation_unavailable", "generation_changed", "store_error", "invalid_payload", "body_limit", "load_error", "load_timeout", "canceled", "expiry_lookup", "expired")).Inc()
	}
}
func (m *ReadCacheMetrics) RecordInvalidation(cache, scope, reason, result string) {
	if m != nil {
		m.Invalidations.WithLabelValues(boundedCacheLabel(cache, "response", "organization", "permission"), boundedCacheLabel(scope, "global", "workspace", "summary", "task", "project"), boundedCacheLabel(reason, "manual", "mutation", "issue", "project", "member", "workspace", "inbox", "organization", "projectauth", "permission", "unknown"), boundedCacheLabel(result, "success", "error")).Inc()
	}
}
func (m *ReadCacheMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Requests, m.Loads, m.Fallbacks, m.Invalidations}
}
