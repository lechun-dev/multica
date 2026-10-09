package handler

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestOrganizationReadCacheIsolationExpiryAndWorkspaceInvalidation(t *testing.T) {
	c := NewOrganizationReadCache(nil, t.Name())
	ctx := context.Background()
	calls := 0
	loader := func(_ context.Context, workspace, user string) ([]string, error) {
		calls++
		return []string{workspace + user}, nil
	}
	load := func(workspace, user string) organizationReadEntry {
		t.Helper()
		e, err := c.load(ctx, workspace, user, loader)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	first := load("a", "reader")
	if ttl := time.Until(first.ExpiresAt); ttl <= 14*time.Second || ttl > frequentReadTTL {
		t.Fatalf("TTL %v", ttl)
	}
	load("a", "reader")
	load("b", "reader")
	load("a", "other")
	if calls != 3 {
		t.Fatalf("isolation calls=%d", calls)
	}
	c.Observe(events.Event{Type: "issue:updated", WorkspaceID: "a"})
	load("a", "reader")
	if calls != 3 {
		t.Fatal("issue update evicted organization IDs")
	}
	c.Observe(events.Event{Type: "organization:updated", WorkspaceID: "a"})
	load("b", "reader")
	load("a", "reader")
	if calls != 4 {
		t.Fatalf("workspace invalidation calls=%d", calls)
	}
	// 2026-10-09 coder(lq): Generation eviction must not revive old ID entries.
	store := c.cache.store.(*memoryFrequentReadStore)
	store.mu.Lock()
	delete(store.values, c.cache.prefix+"workspace:a")
	store.mu.Unlock()
	load("a", "reader")
	if calls != 5 {
		t.Fatal("generation eviction revived stale IDs")
	}
	store.mu.Lock()
	for key, entry := range store.values {
		if strings.Contains(key, ":reader") {
			entry.expires = time.Now().Add(-time.Second)
			store.values[key] = entry
		}
	}
	store.mu.Unlock()
	load("a", "reader")
	if calls != 6 {
		t.Fatal("expired organization entry was served")
	}
}

func TestOrganizationReadCacheConcurrentMissAndInvalidatedFill(t *testing.T) {
	c := NewOrganizationReadCache(nil, t.Name())
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	loader := func(context.Context, string, string) ([]string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []string{"org"}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.load(context.Background(), "ws", "user", loader); err != nil {
				t.Error(err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent loads=%d", calls.Load())
	}
	c.Invalidate("ws")
	started, release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.load(context.Background(), "ws", "user", func(context.Context, string, string) ([]string, error) {
			close(started)
			<-release
			return []string{"stale"}, nil
		})
		done <- err
	}()
	<-started
	c.Invalidate("ws")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("accepted invalidated fill")
	}
}

func TestOrganizationReadCacheImportUsesURLWorkspace(t *testing.T) {
	c := NewOrganizationReadCache(nil, t.Name())
	workspace := uuid.NewString()
	before, _ := c.generation(context.Background(), workspace)
	other, _ := c.generation(context.Background(), "selected-other")
	for _, action := range []string{"import", "sync"} {
		request := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+workspace+"/projectauth/organizations/"+action, nil)
		request.Header.Set("X-Workspace-ID", "selected-other")
		during := ""
		c.MutationMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { during, _ = c.generation(context.Background(), workspace) })).ServeHTTP(httptest.NewRecorder(), request)
		after, _ := c.generation(context.Background(), workspace)
		if during == before || after == during {
			t.Fatal("mutation did not invalidate both sides")
		}
		before = after
	}
	afterOther, _ := c.generation(context.Background(), "selected-other")
	if afterOther != other {
		t.Fatal("invalidated selected workspace instead of URL workspace")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+workspace+"/projectauth/organizations/import/preview", nil)
	c.MutationMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), request)
	after, _ := c.generation(context.Background(), workspace)
	if before != after {
		t.Fatal("preview invalidated cache")
	}
}

func TestOrganizationReadCacheBoundsResponseFreshness(t *testing.T) {
	scope := &organizationReadScope{userID: "user"}
	scope.boundExpiry(time.Now().Add(time.Second))
	scope.boundExpiry(time.Now().Add(14 * time.Second))
	ctx := context.WithValue(context.Background(), organizationReadScopeKey{}, scope)
	h := &Handler{}
	if ttl := h.frequentReadExpiry(ctx, "ws", "user", time.Now()); ttl <= 0 || ttl > time.Second {
		t.Fatalf("layered TTL=%v", ttl)
	}
}

type organizationCountingDB struct {
	dbExecutor
	queries atomic.Int64
}

func (d *organizationCountingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "WITH RECURSIVE user_orgs") {
		d.queries.Add(1)
	}
	return d.dbExecutor.Query(ctx, sql, args...)
}

// 2026-10-09 coder(lq): Compare real list/table/progress responses against live
// recursion and verify that separate read requests reuse one organization load.
func TestOrganizationReadCacheDatabaseListPathsAndLiveWrites(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	counted := &organizationCountingDB{dbExecutor: testPool}
	h := *testHandler
	h.DB = counted
	h.ProjectAuth = projectauth.New(newProjectAuthRepository(counted), true)
	h.FrequentReadCache = nil
	h.OrganizationReadCache = NewOrganizationReadCache(nil, t.Name())
	invoke := func(handler http.HandlerFunc, method, path string, body any, cached bool) []byte {
		t.Helper()
		request := newRequestAs(fixture.reader, method, path, body)
		request.Header.Set("X-Workspace-ID", fixture.ws)
		if cached {
			handler = h.CacheFrequentRead(handler)
		}
		response := runFrequentCache(handler, request)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	compare := func(handler http.HandlerFunc, method, path string, body any) {
		t.Helper()
		before := invoke(handler, method, path, body, false)
		after := invoke(handler, method, path, body, true)
		var old, new any
		if err := json.Unmarshal(before, &old); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after, &new); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(old, new) {
			t.Fatalf("cached policy changed %s\nlive=%s\ncached=%s", path, before, after)
		}
	}
	compare(h.ListIssues, http.MethodGet, "/api/issues?limit=1&offset=1000&sort=status", nil)
	// 2026-10-09 coder(lq): Verify bind positions for both the normal page and the empty-offset count.
	compare(h.ListIssues, http.MethodGet, "/api/issues?limit=1&sort=status", nil)
	spec := issueTableVisibilitySpec()
	rows := issueTableRowsRequest{Query: spec, Group: issueTableGroupSpec{Kind: "none"}, Page: issueTablePageRequest{Limit: 50}}
	groups := issueTableGroupsRequest{Query: spec, Group: issueTableGroupSpec{Kind: "status"}, Page: issueTablePageRequest{Limit: 50}}
	includeTotal := true
	facets := issueTableFacetsRequest{Query: spec, Facets: []issueTableFacetSpec{{Kind: "status"}}, IncludeTotal: &includeTotal}
	compare(h.ListIssueTableRows, http.MethodPost, "/api/issues/table/rows", rows)
	compare(h.ListIssueTableGroups, http.MethodPost, "/api/issues/table/groups", groups)
	compare(h.ListIssueTableFacets, http.MethodPost, "/api/issues/table/facets", facets)
	compare(h.ChildIssueProgress, http.MethodGet, "/api/issues/child-progress", nil)
	// 2026-10-09 coder(lq): Warm reads should no longer contain any recursive organization statement.
	counted.queries.Store(0)
	invoke(h.ListIssues, http.MethodGet, "/api/issues?limit=1&sort=position", nil, true)
	invoke(h.ListIssueTableRows, http.MethodPost, "/api/issues/table/rows", rows, true)
	invoke(h.ChildIssueProgress, http.MethodGet, "/api/issues/child-progress", nil, true)
	if n := counted.queries.Load(); n != 0 {
		t.Fatalf("warm reads ran %d recursive statements", n)
	}
	repo := &projectAuthRepository{db: counted}
	cachedCtx := context.WithValue(context.Background(), organizationReadScopeKey{}, &organizationReadScope{cache: h.OrganizationReadCache, userID: fixture.reader, loader: repo.listUserOrganizations})
	old, err := repo.ListUserOrganizations(cachedCtx, fixture.ws, fixture.reader)
	if err != nil || len(old) == 0 {
		t.Fatalf("warm organization IDs=%v err=%v", old, err)
	}
	fixture.fx.Exec(t, "UPDATE projectauth_organizations SET status='disabled' WHERE workspace_id=$1", fixture.ws)
	// 2026-10-09 coder(lq): Operation/background contexts always see the current directory immediately.
	live, err := repo.ListUserOrganizations(context.Background(), fixture.ws, fixture.reader)
	if err != nil || len(live) != 0 {
		t.Fatalf("live operation organizations=%v err=%v", live, err)
	}
	stale, err := repo.ListUserOrganizations(cachedCtx, fixture.ws, fixture.reader)
	if err != nil || !reflect.DeepEqual(stale, old) {
		t.Fatalf("expected short read cache before event: %v %v", stale, err)
	}
	h.OrganizationReadCache.Observe(events.Event{Type: "organization:updated", WorkspaceID: fixture.ws})
	compare(h.ListIssues, http.MethodGet, "/api/issues?limit=10", nil)
	fresh, err := repo.ListUserOrganizations(cachedCtx, fixture.ws, fixture.reader)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("invalidation organizations=%v err=%v", fresh, err)
	}
	// 2026-10-09 coder(lq): A failed cache must preserve the original recursive SQL path.
	store := newFakeAgentMetricsCacheStore()
	store.getErr = fmt.Errorf("redis unavailable")
	h.OrganizationReadCache.cache.store = store
	compare(h.ListIssues, http.MethodGet, "/api/issues?limit=10", nil)
}

func TestOrganizationReadCacheSharedStoreAndFailures(t *testing.T) {
	first := NewOrganizationReadCache(nil, t.Name())
	second := NewOrganizationReadCache(nil, t.Name())
	second.cache.store = first.cache.store
	calls := 0
	loader := func(context.Context, string, string) ([]string, error) { calls++; return []string{"org"}, nil }
	for _, cache := range []*OrganizationReadCache{first, second} {
		if _, err := cache.load(context.Background(), "ws", "user", loader); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("nodes did not share organization IDs")
	}
	second.Invalidate("ws")
	if _, err := first.load(context.Background(), "ws", "user", loader); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("node ignored shared invalidation")
	}
	first.Invalidate("ws")
	failure := func(context.Context, string, string) ([]string, error) {
		return nil, fmt.Errorf("database unavailable")
	}
	if _, err := first.load(context.Background(), "ws", "user", failure); err == nil {
		t.Fatal("loader error accepted")
	}
	if _, err := first.load(context.Background(), "ws", "user", loader); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("loader error populated cache")
	}
	store := newFakeAgentMetricsCacheStore()
	store.setErr = fmt.Errorf("redis write unavailable")
	first.cache.store = store
	first.Invalidate("ws")
	if _, err := first.load(context.Background(), "ws", "user", loader); err == nil {
		t.Fatal("failed invalidation reused cache")
	}
}

func TestOrganizationReadCacheOptInExcludesMachineActors(t *testing.T) {
	h := &Handler{OrganizationReadCache: NewOrganizationReadCache(nil, t.Name())}
	optedIn := false
	handler := h.CacheFrequentRead(func(w http.ResponseWriter, r *http.Request) {
		_, optedIn = r.Context().Value(organizationReadScopeKey{}).(*organizationReadScope)
		fmt.Fprint(w, `{}`)
	})
	human := frequentCacheRequest("user", "workspace", "", "{}")
	runFrequentCache(handler, human)
	if !optedIn {
		t.Fatal("human read did not opt in")
	}
	for _, header := range []string{"X-Actor-Source", "X-Agent-ID"} {
		request := frequentCacheRequest("user", "workspace", "", "{}")
		if header == "X-Actor-Source" {
			request.Header.Set(header, "task_token")
		} else {
			request.Header.Set(header, "agent")
		}
		runFrequentCache(handler, request)
		if optedIn {
			t.Fatalf("machine credential %s opted in", header)
		}
	}
}

// 2026-10-09 coder(lq): A canceled organization preload must leave the table
// transaction usable so its original recursive ACL can still execute.
type organizationTimeoutDB struct {
	dbExecutor
	loads atomic.Int64
}

func (d *organizationTimeoutDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "WITH RECURSIVE user_orgs") {
		d.loads.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.dbExecutor.Query(ctx, sql, args...)
}
func TestOrganizationReadCachePreloadTimeoutPreservesTableTransaction(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	h := *testHandler
	delayed := &organizationTimeoutDB{dbExecutor: testPool}
	h.DB = delayed
	h.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	h.FrequentReadCache = nil
	h.OrganizationReadCache = NewOrganizationReadCache(nil, t.Name())
	body := issueTableRowsRequest{Query: issueTableVisibilitySpec(), Group: issueTableGroupSpec{Kind: "none"}, Page: issueTablePageRequest{Limit: 50}}
	request := issueTableVisibilityRequest(t, fixture.reader, fixture.ws, body)
	response := runFrequentCache(h.CacheFrequentRead(h.ListIssueTableRows), request)
	if response.Code != 200 {
		t.Fatalf("table after preload timeout: %d %s", response.Code, response.Body.String())
	}
	var rows issueTableRowsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if rows.Total != int64(fixture.expected) || delayed.loads.Load() != 1 {
		t.Fatalf("total=%d want=%d preload calls=%d", rows.Total, fixture.expected, delayed.loads.Load())
	}
}

func TestOrganizationReadCacheCanonicalUUIDInvalidation(t *testing.T) {
	c := NewOrganizationReadCache(nil, t.Name())
	workspace, user := uuid.NewString(), uuid.NewString()
	calls := 0
	loader := func(context.Context, string, string) ([]string, error) { calls++; return nil, nil }
	for _, ws := range []string{workspace, strings.ToUpper(workspace)} {
		if _, err := c.load(context.Background(), ws, user, loader); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("UUID casing created separate caches")
	}
	c.Invalidate(workspace)
	if _, err := c.load(context.Background(), strings.ToUpper(workspace), user, loader); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("canonical event failed to invalidate uppercase header")
	}
}
