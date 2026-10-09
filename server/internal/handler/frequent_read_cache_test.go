package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

func frequentCacheRequest(user, workspace, query, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/issues/table/rows"+query, strings.NewReader(body))
	request.Header.Set("X-User-ID", user)
	request.Header.Set("X-Workspace-ID", workspace)
	return request
}
func runFrequentCache(handler http.HandlerFunc, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// 2026-10-09 coder(lq): User/workspace/filter/body boundaries must survive
// caching; expiration and invalidation must restore the original handler read.
func TestFrequentReadCacheIsolationTTLAndInvalidation(t *testing.T) {
	cache := NewFrequentReadCache(nil, "isolation")
	h := &Handler{FrequentReadCache: cache}
	calls := 0
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"load":%d}`, calls)
	})
	first := runFrequentCache(cached, frequentCacheRequest("reader-a", "ws-a", "?limit=1", `{}`))
	second := runFrequentCache(cached, frequentCacheRequest("reader-a", "ws-a", "?limit=1", `{}`))
	if calls != 1 || first.Body.String() != second.Body.String() {
		t.Fatal("identical request missed cache")
	}
	for _, request := range []*http.Request{
		frequentCacheRequest("reader-b", "ws-a", "?limit=1", `{}`),
		frequentCacheRequest("reader-a", "ws-b", "?limit=1", `{}`),
		frequentCacheRequest("reader-a", "ws-a", "?limit=2", `{}`),
		frequentCacheRequest("reader-a", "ws-a", "?limit=1", `{"filters":{"status":"todo"}}`),
	} {
		runFrequentCache(cached, request)
	}
	if calls != 5 {
		t.Fatalf("isolated loads=%d want 5", calls)
	}
	store := cache.store.(*memoryFrequentReadStore)
	store.mu.Lock()
	for key, entry := range store.values {
		if entry.expires.IsZero() || strings.HasSuffix(key, "generation") {
			continue
		}
		remaining := time.Until(entry.expires)
		if remaining <= 14*time.Second || remaining > frequentReadTTL {
			t.Errorf("TTL=%v want 15 seconds", remaining)
		}
		entry.expires = time.Now().Add(-time.Second)
		store.values[key] = entry
	}
	store.mu.Unlock()
	runFrequentCache(cached, frequentCacheRequest("reader-a", "ws-a", "?limit=1", `{}`))
	if calls != 6 {
		t.Fatal("expired entry survived")
	}
	cache.Observe(events.Event{Type: "issue:updated"})
	runFrequentCache(cached, frequentCacheRequest("reader-a", "ws-a", "?limit=1", `{}`))
	if calls != 7 {
		t.Fatal("mutation did not invalidate entry")
	}
}

func TestFrequentReadCacheCoalescesConcurrentMisses(t *testing.T) {
	h := &Handler{FrequentReadCache: NewFrequentReadCache(nil, "concurrent")}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":7}`))
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
			if got.Code != http.StatusOK || got.Body.String() != `{"total":7}` {
				t.Errorf("response=%d %s", got.Code, got.Body.String())
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("database loads=%d want 1", calls.Load())
	}
}

func TestFrequentReadCacheSharedGenerationAndMutationRoutes(t *testing.T) {
	shared := &memoryFrequentReadStore{values: make(map[string]memoryFrequentReadEntry)}
	a, b := NewFrequentReadCache(nil, "shared"), NewFrequentReadCache(nil, "shared")
	a.store, b.store = shared, shared
	a.Invalidate()
	h := &Handler{FrequentReadCache: b}
	calls := 0
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	load := func() { runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`)) }
	load()
	load()
	mutate := a.MutationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	mutate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/api/issues/task/access-control", nil))
	load()
	if calls != 2 {
		t.Fatal("other API node retained pre-revocation cache")
	}
	mutate.ServeHTTP(httptest.NewRecorder(), frequentCacheRequest("reader", "ws", "", `{}`))
	load()
	if calls != 2 {
		t.Fatal("POST table read invalidated its own cache")
	}
}

func TestFrequentReadCacheDoesNotCacheErrorsOrMachineActors(t *testing.T) {
	h := &Handler{FrequentReadCache: NewFrequentReadCache(nil, "errors")}
	calls := 0
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	})
	for range 2 {
		response := runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
		if response.Code != 503 || response.Header().Get("Retry-After") != "1" {
			t.Fatal("error response changed")
		}
	}
	if calls != 2 {
		t.Fatal("server errors were cached")
	}
	cached = h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) })
	for range 2 {
		request := frequentCacheRequest("reader", "ws", "", `{}`)
		request.Header.Set("X-Actor-Source", "task_token")
		runFrequentCache(cached, request)
	}
	if calls != 4 {
		t.Fatal("task token read was cached")
	}
	store := newFakeAgentMetricsCacheStore()
	store.getErr = fmt.Errorf("redis unavailable")
	h.FrequentReadCache.store = store
	for range 2 {
		runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
	}
	if calls != 6 {
		t.Fatal("Redis failure did not fall back to original read")
	}
}

type frequentExpiryDB struct {
	dbExecutor
	expiry     time.Time
	fail       bool
	workspaces []string
}

func (db *frequentExpiryDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	db.workspaces = append(db.workspaces, fmt.Sprint(args[0]))
	return frequentExpiryRow{db.expiry, db.fail}
}

type frequentExpiryRow struct {
	expiry time.Time
	fail   bool
}

func (row frequentExpiryRow) Scan(dest ...any) error {
	if row.fail {
		return fmt.Errorf("expiry lookup failed")
	}
	*dest[0].(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: row.expiry, Valid: !row.expiry.IsZero()}
	return nil
}

func TestFrequentReadCacheCapsTTLAtGrantExpiry(t *testing.T) {
	cache := NewFrequentReadCache(nil, "expiry")
	db := &frequentExpiryDB{expiry: time.Now().Add(3 * time.Second)}
	h := &Handler{FrequentReadCache: cache, DB: db, ProjectAuth: projectauth.NewWithRollout(nil, projectauth.RolloutReader)}
	calls := 0
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) })
	request := frequentCacheRequest("reader", "ws", "", `{}`)
	request.URL.Path = "/api/inbox/unread-summary"
	runFrequentCache(cached, request)
	if len(db.workspaces) != 1 || db.workspaces[0] != "reader" {
		t.Fatalf("summary expiry lookup was scoped to current workspace: %v", db.workspaces)
	}
	store := cache.store.(*memoryFrequentReadStore)
	store.mu.Lock()
	for key, entry := range store.values {
		if !entry.expires.IsZero() && !strings.HasSuffix(key, "generation") && time.Until(entry.expires) > 3*time.Second {
			t.Fatal("cached beyond grant expiry")
		}
	}
	store.mu.Unlock()
	cache.Invalidate()
	db.expiry = time.Now().Add(-time.Second)
	for range 2 {
		runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
	}
	if calls != 3 {
		t.Fatal("expired during query response was cached")
	}
	db.fail = true
	for range 2 {
		runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
	}
	if calls != 5 {
		t.Fatal("failed expiry lookup populated cache")
	}
}

// 2026-10-09 coder(lq): A late fill must not resurrect pre-revocation data;
// retries of POST list twins must retain the complete original request body.
func TestFrequentReadCacheRejectsFillAfterInvalidation(t *testing.T) {
	cache := NewFrequentReadCache(nil, "late-fill")
	h := &Handler{FrequentReadCache: cache}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		input, err := io.ReadAll(r.Body)
		if err != nil || string(input) != `{"page":1}` {
			t.Errorf("retry lost POST body: %s, %v", input, err)
		}
		if n == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"load":%d}`, n)
	})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{"page":1}`)) }()
	<-started
	cache.Invalidate()
	close(release)
	response := <-result
	if response.Body.String() != `{"load":2}` {
		t.Fatalf("replayed pre-revocation response: %s", response.Body.String())
	}
	runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{"page":1}`))
	runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{"page":1}`))
	if calls.Load() != 3 {
		t.Fatalf("late fill entered new generation: loads=%d", calls.Load())
	}
}

func TestFrequentReadCacheMissingGenerationFailsClosed(t *testing.T) {
	cache := NewFrequentReadCache(nil, "missing-generation")
	h := &Handler{FrequentReadCache: cache}
	calls := 0
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) })
	runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
	store := cache.store.(*memoryFrequentReadStore)
	store.mu.Lock()
	delete(store.values, cache.prefix+"generation")
	store.mu.Unlock()
	for range 2 {
		runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`))
	}
	if calls != 3 {
		t.Fatal("missing generation reused stale cache")
	}
}

func TestFrequentReadCacheDatabaseExpiryIncludesOtherWorkspaces(t *testing.T) {
	reader := dbfx.User(t, "Cached summary reader", "cached-summary-reader@example.test")
	for _, seconds := range []int{10, 3} {
		ws := dbfx.Workspace(t, fmt.Sprintf("Cache expiry %d", seconds), fmt.Sprintf("cache-expiry-%d", seconds))
		fx := testutil.New(testPool, ws, testUserID)
		fx.Member(t, ws, reader, "member")
		task := fx.Issue(t, "Timed grant")
		grant := fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": ws, "issue_id": task, "subject_type": "user", "subject_id": reader, "role_key": "viewer"})
		fx.InsertNoID(t, "projectauth_grant_constraints", testutil.Cols{"workspace_id": ws, "grant_id": grant, "expires_at": time.Now().Add(time.Duration(seconds) * time.Second)}, "workspace_id=$1 AND grant_id=$2", ws, grant)
	}
	h := *testHandler
	h.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	ttl := h.frequentReadExpiry(t.Context(), "", reader, time.Now())
	if ttl <= 0 || ttl > 3*time.Second {
		t.Fatalf("summary grant expiry TTL=%v want (0,3s]", ttl)
	}
}

// 2026-10-09 coder(lq): A mutation on another API node must clear the affected
// workspace and account summary while retaining unrelated workspace responses.
func TestFrequentReadCacheWorkspaceIsolationAndSummary(t *testing.T) {
	a, b := NewFrequentReadCache(nil, t.Name()), NewFrequentReadCache(nil, t.Name())
	b.store = a.store
	b.Metrics = obsmetrics.NewReadCacheMetrics()
	h := &Handler{FrequentReadCache: b}
	calls := map[string]int{}
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path + organizationCacheID(r.Header.Get("X-Workspace-ID"))
		calls[key]++
		fmt.Fprintf(w, `{"load":%d}`, calls[key])
	})
	workspace := uuid.NewString()
	read := func(path, ws string) string {
		t.Helper()
		req := frequentCacheRequest("reader", ws, "", `{}`)
		req.URL.Path = path
		return runFrequentCache(cached, req).Body.String()
	}
	for range 2 {
		read("/api/issues/table/rows", workspace)
		read("/api/issues/table/rows", "other-workspace")
		read("/api/inbox/unread-summary", "other-workspace")
	}
	a.Observe(events.Event{Type: "issue:updated", WorkspaceID: workspace})
	if got := read("/api/issues/table/rows", strings.ToUpper(workspace)); got != `{"load":2}` {
		t.Fatalf("uppercase workspace returned old response: %s", got)
	}
	if got := read("/api/issues/table/rows", workspace); got != `{"load":2}` {
		t.Fatalf("canonical UUID did not reuse fresh uppercase response: %s", got)
	}
	if got := read("/api/issues/table/rows", "other-workspace"); got != `{"load":1}` {
		t.Fatalf("other workspace was evicted: %s", got)
	}
	if got := read("/api/inbox/unread-summary", "other-workspace"); got != `{"load":2}` {
		t.Fatalf("cross-workspace summary survived mutation: %s", got)
	}
	a.Observe(events.Event{Type: "permission:updated"})
	if got := read("/api/issues/table/rows", "other-workspace"); got != `{"load":2}` {
		t.Fatalf("unknown-scope permission mutation did not clear all responses: %s", got)
	}
	if got := promtest.ToFloat64(b.Metrics.Requests.WithLabelValues("response", "table_rows", "hit")); got != 4 {
		t.Fatalf("table hit metric=%v want 4", got)
	}
}

func TestFrequentReadCacheMutationUsesURLWorkspaceAndReadPosts(t *testing.T) {
	c := NewFrequentReadCache(nil, t.Name())
	c.Metrics = obsmetrics.NewReadCacheMetrics()
	workspace, selected := uuid.NewString(), uuid.NewString()
	version := func(ws string) string {
		t.Helper()
		v, err := c.generation(t.Context(), ws)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before, other, summary := version(workspace), version(selected), version("")
	during := ""
	mutate := c.MutationMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { during = version(workspace) }))
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+workspace+"/projectauth/organizations/import", nil)
	req.Header.Set("X-Workspace-ID", selected)
	mutate.ServeHTTP(httptest.NewRecorder(), req)
	if during == before || version(workspace) == during || version(selected) != other || version("") == summary {
		t.Fatal("mutation scope or before/after invalidation incorrect")
	}
	if got := promtest.ToFloat64(c.Metrics.Invalidations.WithLabelValues("response", "workspace", "mutation", "success")); got != 2 {
		t.Fatalf("workspace invalidation metric=%v want 2", got)
	}
	before = version(workspace)
	for _, path := range []string{"/api/issues/query", "/api/issues/table/rows", "/api/issues/table/groups", "/api/issues/table/facets"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("X-Workspace-ID", workspace)
		mutate.ServeHTTP(httptest.NewRecorder(), req)
	}
	if version(workspace) != before {
		t.Fatal("read POST invalidated cache")
	}
	// 2026-10-09 coder(lq): Unresolved URL scopes require global invalidation.
	mutate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/api/workspaces/unknown", nil))
	if version(selected) == other {
		t.Fatal("unknown URL scope did not trigger global invalidation")
	}
}

func TestFrequentReadCacheScopedGenerationEvictionAndLateFill(t *testing.T) {
	c := NewFrequentReadCache(nil, t.Name())
	h := &Handler{FrequentReadCache: c}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cached := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release
		}
		fmt.Fprintf(w, `{"load":%d}`, n)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`)) }()
	<-started
	c.InvalidateWorkspace("ws")
	close(release)
	if got := (<-done).Body.String(); got != `{"load":2}` {
		t.Fatalf("accepted stale scoped fill: %s", got)
	}
	request := func() { runFrequentCache(cached, frequentCacheRequest("reader", "ws", "", `{}`)) }
	request()
	request()
	store := c.store.(*memoryFrequentReadStore)
	store.mu.Lock()
	delete(store.values, c.scopeGenerationKey("ws"))
	store.mu.Unlock()
	request()
	if calls.Load() != 4 {
		t.Fatalf("evicted generation revived stale response: calls=%d", calls.Load())
	}
}

// 2026-10-09 coder(lq): Revoking real organization access must refresh both
// cache layers and match the uncached permission-filtered table response.
func TestFrequentReadCacheDatabaseOrganizationRevocation(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	h := *testHandler
	h.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	h.FrequentReadCache = NewFrequentReadCache(nil, t.Name())
	h.OrganizationReadCache = NewOrganizationReadCache(nil, t.Name())
	body := issueTableRowsRequest{Query: issueTableVisibilitySpec(), Group: issueTableGroupSpec{Kind: "none"}, Page: issueTablePageRequest{Limit: 50}}
	read := func(cached bool) issueTableRowsResponse {
		t.Helper()
		handler := h.ListIssueTableRows
		if cached {
			handler = h.CacheFrequentRead(handler)
		}
		response := runFrequentCache(handler, issueTableVisibilityRequest(t, fixture.reader, fixture.ws, body))
		if response.Code != http.StatusOK {
			t.Fatalf("rows status=%d body=%s", response.Code, response.Body.String())
		}
		var rows issueTableRowsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := read(true)
	if before.Total != int64(fixture.expected) {
		t.Fatalf("warm total=%d want %d", before.Total, fixture.expected)
	}
	fixture.fx.Exec(t, "UPDATE projectauth_organizations SET status='disabled' WHERE workspace_id=$1", fixture.ws)
	if cached := read(true); !reflect.DeepEqual(cached, before) {
		t.Fatal("warm cache fixture did not reuse response")
	}
	event := events.Event{Type: "organization:updated", WorkspaceID: fixture.ws}
	h.FrequentReadCache.Observe(event)
	h.OrganizationReadCache.Observe(event)
	actual, live := read(true), read(false)
	if actual.Total >= before.Total || !reflect.DeepEqual(actual, live) {
		t.Fatalf("revocation drift: before=%d cached=%d live=%d", before.Total, actual.Total, live.Total)
	}
}
