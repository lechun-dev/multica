package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

func permissionCacheRequest(user, workspace, issue, endpoint string) *http.Request {
	req := issueAccessHTTPRequest(user, workspace, http.MethodGet, issue, "", nil)
	req.URL.Path += "/" + endpoint
	return req
}

func permissionCacheHandler(t *testing.T) *Handler {
	t.Helper()
	h := *testHandler
	h.ProjectAuth = projectauth.NewWithRollout(&projectAuthRepository{db: testPool}, projectauth.RolloutRestricted)
	h.EffectiveIssueAccess = projectauth.NewEffectiveAccessResolver(&projectAuthRepository{db: testPool})
	h.PermissionReadCache = NewPermissionReadCache(nil, t.Name())
	h.PermissionReadCache.SetMetrics(obsmetrics.NewReadCacheMetrics())
	return &h
}

// 2026-10-09 coder(lq): Exercise the real APIs so cached payloads preserve the
// authorization rules and schemas of all six independent permission reads.
func TestPermissionReadCacheDatabaseEndpointsAndUserIsolation(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	var task, project string
	fixture.fx.QueryRow(t, "SELECT id::text, project_id::text FROM issue WHERE workspace_id=$1 AND title='Shared todo'", fixture.ws).Scan(&task, &project)
	cases := []struct {
		name, path, id string
		handler        http.HandlerFunc
	}{
		{"effective_access", "/api/issues/" + task + "/effective-access", task, h.GetIssueEffectiveAccess},
		{"access_control", "/api/issues/" + task + "/access-control", task, h.GetIssueAccessControl},
		{"issue_grants", "/api/issues/" + task + "/access-grants", task, h.ListIssueAccessGrants},
		{"project_grants", "/api/projects/" + project + "/access-grants", project, h.ListProjectAccessGrants},
		{"project_roles", "/api/project-permission-roles", "", h.ListProjectPermissionRoles},
		{"task_roles", "/api/task-permission-roles", "", h.ListTaskPermissionRoles},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			cached := h.CachePermissionRead(func(w http.ResponseWriter, r *http.Request) { calls++; tc.handler(w, r) })
			read := func(user string) *httptest.ResponseRecorder {
				req := newRequestAs(user, http.MethodGet, tc.path, nil)
				req.Header.Set("X-Workspace-ID", fixture.ws)
				return runFrequentCache(cached, withURLParam(req, "id", tc.id))
			}
			first, second := read(testUserID), read(testUserID)
			if first.Code != 200 || second.Code != 200 || calls != 1 || first.Body.String() != second.Body.String() {
				t.Fatalf("cache changed %s: status=%d/%d loads=%d body=%s", tc.name, first.Code, second.Code, calls, first.Body.String())
			}
			reader := read(fixture.reader)
			if calls != 2 {
				t.Fatal("shared another user's permission response")
			}
			if tc.name == "access_control" || tc.name == "issue_grants" {
				if reader.Code != 403 {
					t.Fatalf("viewer received manager response: %d %s", reader.Code, reader.Body.String())
				}
				read(fixture.reader)
				if calls != 3 {
					t.Fatal("cached permission denial")
				}
			}
			if got := promtest.ToFloat64(h.PermissionReadCache.cache.Metrics.Requests.WithLabelValues("permission", tc.name, "hit")); got != 1 {
				t.Fatalf("permission hit metric=%v", got)
			}
		})
	}
}

func TestPermissionReadCacheTaskRevocationKeepsOtherTasksAndRoles(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	var task, other, project string
	fixture.fx.QueryRow(t, "SELECT id::text,project_id::text FROM issue WHERE workspace_id=$1 AND title='Shared todo'", fixture.ws).Scan(&task, &project)
	fixture.fx.QueryRow(t, "SELECT id::text FROM issue WHERE workspace_id=$1 AND title='Shared done'", fixture.ws).Scan(&other)
	grant := fixture.fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": fixture.ws, "project_id": project, "issue_id": task, "subject_type": "user", "subject_id": fixture.reader, "role_key": "manager"})
	calls := map[string]int{}
	cached := h.CachePermissionRead(func(w http.ResponseWriter, r *http.Request) { calls[r.URL.Path]++; h.GetIssueEffectiveAccess(w, r) })
	read := func(id string) *httptest.ResponseRecorder {
		return runFrequentCache(cached, permissionCacheRequest(fixture.reader, fixture.ws, id, "effective-access"))
	}
	first := read(task)
	if first.Code != 200 || read(other).Code != 200 {
		t.Fatal("warm permission failed")
	}
	hasManage := func(response *httptest.ResponseRecorder) bool {
		t.Helper()
		var access projectauth.EffectiveIssueAccess
		if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
			t.Fatal(err)
		}
		for _, permission := range access.Permissions {
			if permission == projectauth.IssueManage {
				return true
			}
		}
		return false
	}
	if !hasManage(first) {
		t.Fatal("fixture has no direct manager access")
	}
	rolesBefore, _ := h.PermissionReadCache.cache.generation(t.Context(), fixture.ws)
	mutate := h.PermissionReadMutationMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fixture.fx.Exec(t, "DELETE FROM projectauth_access_grants WHERE id=$1", grant)
	}))
	req := issueAccessHTTPRequest(testUserID, fixture.ws, http.MethodDelete, task, "", nil)
	req.URL.Path += "/access-grants"
	mutate.ServeHTTP(httptest.NewRecorder(), req)
	if read(other).Code != 200 || calls["/api/issues/"+other+"/effective-access"] != 1 {
		t.Fatal("task mutation evicted unrelated task")
	}
	refreshed := read(task)
	if refreshed.Code != 200 || hasManage(refreshed) || calls["/api/issues/"+task+"/effective-access"] != 2 {
		t.Fatal("task mutation retained old permission response")
	}
	rolesAfter, _ := h.PermissionReadCache.cache.generation(t.Context(), fixture.ws)
	if rolesBefore != rolesAfter {
		t.Fatal("task mutation cleared workspace role catalog")
	}
	// 2026-10-09 coder(lq): Organization revocation must refresh every dependent task.
	fixture.fx.Exec(t, "UPDATE projectauth_organizations SET status='disabled' WHERE workspace_id=$1", fixture.ws)
	h.PermissionReadCache.Observe(events.Event{Type: "organization:updated", WorkspaceID: fixture.ws})
	if response := read(task); response.Code != 403 {
		t.Fatalf("revoked organization permission survived: %d %s", response.Code, response.Body.String())
	}
}

func TestPermissionReadCacheParentAndProjectDependencies(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	parent := fixture.fx.Issue(t, "Permission parent")
	child := fixture.fx.Issue(t, "Permission child", testutil.Cols{"parent_issue_id": parent})
	grant := fixture.fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": fixture.ws, "issue_id": parent, "subject_type": "user", "subject_id": fixture.reader, "role_key": "viewer"})
	cached := h.CachePermissionRead(h.GetIssueEffectiveAccess)
	read := func(id string) *httptest.ResponseRecorder {
		return runFrequentCache(cached, permissionCacheRequest(fixture.reader, fixture.ws, id, "effective-access"))
	}
	if response := read(child); response.Code != 200 {
		t.Fatalf("parent inheritance: %d %s", response.Code, response.Body.String())
	}
	fixture.fx.Exec(t, "DELETE FROM projectauth_issue_access_grants WHERE id=$1", grant)
	h.PermissionReadCache.Observe(events.Event{Type: "issue:updated", WorkspaceID: fixture.ws, Payload: map[string]any{"issue": IssueResponse{ID: parent}}})
	if response := read(child); response.Code != 403 {
		t.Fatalf("parent revoke retained child access: %d %s", response.Code, response.Body.String())
	}
	var project, task string
	fixture.fx.QueryRow(t, "SELECT project_id::text,id::text FROM issue WHERE workspace_id=$1 AND title='Shared todo'", fixture.ws).Scan(&project, &task)
	if read(task).Code != 200 {
		t.Fatal("project warm permission failed")
	}
	fixture.fx.Exec(t, "DELETE FROM projectauth_access_grants WHERE workspace_id=$1 AND project_id=$2", fixture.ws, project)
	h.PermissionReadCache.Observe(events.Event{Type: "project:updated", WorkspaceID: fixture.ws, Payload: map[string]any{"project_id": project}})
	if response := read(task); response.Code != 403 {
		t.Fatalf("project revoke retained task access: %d %s", response.Code, response.Body.String())
	}
}

// 2026-10-09 coder(lq): Binding reads must prevent replay even before a move
// event arrives; deleted tasks must preserve the original endpoint's 404.
func TestPermissionReadCacheChangedBindingsAndDeletedTask(t *testing.T) {
	fixture := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	parent := fixture.fx.Issue(t, "Visible cache parent")
	hiddenParent := fixture.fx.Issue(t, "Hidden cache parent")
	child := fixture.fx.Issue(t, "Cache child", testutil.Cols{"parent_issue_id": parent})
	fixture.fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": fixture.ws, "issue_id": parent, "subject_type": "user", "subject_id": fixture.reader, "role_key": "viewer"})
	cached := h.CachePermissionRead(h.GetIssueEffectiveAccess)
	read := func(id string) *httptest.ResponseRecorder {
		return runFrequentCache(cached, permissionCacheRequest(fixture.reader, fixture.ws, id, "effective-access"))
	}
	for range 2 {
		if got := read(child); got.Code != http.StatusOK {
			t.Fatalf("warm child: %d %s", got.Code, got.Body.String())
		}
	}
	fixture.fx.Exec(t, "UPDATE issue SET parent_issue_id=$1 WHERE id=$2", hiddenParent, child)
	if got := read(child); got.Code != http.StatusForbidden {
		t.Fatalf("changed parent retained cached access: %d %s", got.Code, got.Body.String())
	}
	var task, hiddenProject string
	fixture.fx.QueryRow(t, "SELECT id::text FROM issue WHERE workspace_id=$1 AND title='Shared todo'", fixture.ws).Scan(&task)
	fixture.fx.QueryRow(t, "SELECT id::text FROM project WHERE workspace_id=$1 AND title='Hidden project'", fixture.ws).Scan(&hiddenProject)
	for range 2 {
		if got := read(task); got.Code != http.StatusOK {
			t.Fatalf("warm project task: %d %s", got.Code, got.Body.String())
		}
	}
	fixture.fx.Exec(t, "UPDATE issue SET project_id=$1 WHERE id=$2", hiddenProject, task)
	if got := read(task); got.Code != http.StatusForbidden {
		t.Fatalf("changed project retained cached access: %d %s", got.Code, got.Body.String())
	}
	fixture.fx.Exec(t, "UPDATE issue SET parent_issue_id=$1 WHERE id=$2", parent, child)
	if got := read(child); got.Code != http.StatusOK {
		t.Fatalf("restored parent: %d %s", got.Code, got.Body.String())
	}
	fixture.fx.Exec(t, "DELETE FROM issue WHERE id=$1", child)
	if got := read(child); got.Code != http.StatusNotFound {
		t.Fatalf("deleted task retained cached access: %d %s", got.Code, got.Body.String())
	}
}

// 2026-10-09 coder(lq): Expiry has no event. Test both a warmed response and
// a fill that finishes after the grant expires, including singleflight delivery.
func TestPermissionReadCacheTimedGrantAndExpiredFill(t *testing.T) {
	for _, phase := range []projectauth.RolloutPhase{projectauth.RolloutOff, projectauth.RolloutShadow, projectauth.RolloutRestricted} {
		for _, delayFill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delayed=%t", phase, delayFill), func(t *testing.T) {
				fixture := newIssueTableVisibilityFixture(t)
				h := permissionCacheHandler(t)
				h.ProjectAuth = projectauth.NewWithRollout(&projectAuthRepository{db: testPool}, phase)
				task := fixture.fx.Issue(t, "Timed permission")
				grant := fixture.fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": fixture.ws, "issue_id": task, "subject_type": "user", "subject_id": fixture.reader, "role_key": "viewer"})
				expires := time.Now().Add(500 * time.Millisecond)
				fixture.fx.InsertNoID(t, "projectauth_grant_constraints", testutil.Cols{"workspace_id": fixture.ws, "grant_id": grant, "expires_at": expires}, "workspace_id=$1 AND grant_id=$2", fixture.ws, grant)
				calls := 0
				cached := h.CachePermissionRead(func(w http.ResponseWriter, r *http.Request) {
					calls++
					h.GetIssueEffectiveAccess(w, r)
					if delayFill && calls == 1 {
						<-time.After(time.Until(expires) + 10*time.Millisecond)
					}
				})
				read := func() *httptest.ResponseRecorder {
					return runFrequentCache(cached, permissionCacheRequest(fixture.reader, fixture.ws, task, "effective-access"))
				}
				first := read()
				if delayFill {
					if first.Code != 403 || calls != 2 {
						t.Fatalf("expired fill returned authorization: %d loads=%d", first.Code, calls)
					}
				} else {
					if first.Code != 200 || read().Code != 200 || calls != 1 {
						t.Fatal("timed permission not cached")
					}
					<-time.After(time.Until(expires) + 10*time.Millisecond)
					if response := read(); response.Code != 403 {
						t.Fatalf("expired cached permission: %d %s", response.Code, response.Body.String())
					}
				}
			})
		}
	}
}

func TestPermissionReadCacheCoalescesAndBypassesMachinesAndOutages(t *testing.T) {
	h := &Handler{PermissionReadCache: NewPermissionReadCache(nil, t.Name())}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	cached := h.CachePermissionRead(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, `{"roles":[]}`)
	})
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/task-permission-roles", nil)
		req.Header.Set("X-User-ID", "reader")
		req.Header.Set("X-Workspace-ID", "ws")
		return req
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if runFrequentCache(cached, request()).Code != 200 {
				t.Error("permission read failed")
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("coalesced permission loads=%d", calls.Load())
	}
	for range 2 {
		req := request()
		req.Header.Set("X-Actor-Source", "task_token")
		runFrequentCache(cached, req)
	}
	if calls.Load() != 3 {
		t.Fatal("machine permission read used human cache")
	}
	store := newFakeAgentMetricsCacheStore()
	store.getErr = fmt.Errorf("cache offline")
	h.PermissionReadCache.cache.store = store
	for range 2 {
		runFrequentCache(cached, request())
	}
	if calls.Load() != 5 {
		t.Fatal("cache outage did not execute live permission handler")
	}
}

func TestPermissionReadCacheSharedGenerationsAndNonPermissionEvents(t *testing.T) {
	a, b := NewPermissionReadCache(nil, t.Name()), NewPermissionReadCache(nil, t.Name())
	b.cache.store = a.cache.store
	version := func(scope string) string {
		t.Helper()
		v, err := b.cache.generation(context.Background(), scope)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	workspace, task, other := testWorkspaceID, dbfx.Issue(t, "Permission metadata"), dbfx.Issue(t, "Other permission metadata")
	before, otherBefore := version(permissionResourceScope(workspace, "task", task)), version(permissionResourceScope(workspace, "task", other))
	for _, event := range []string{"inbox:read", "task:progress", "agent:status"} {
		a.Observe(events.Event{Type: event, WorkspaceID: workspace})
	}
	if version(permissionResourceScope(workspace, "task", task)) != before {
		t.Fatal("non-permission event evicted cache")
	}
	a.Observe(events.Event{Type: "comment:updated", WorkspaceID: workspace, Payload: map[string]any{"comment": CommentResponse{IssueID: task}}})
	if version(permissionResourceScope(workspace, "task", task)) == before || version(permissionResourceScope(workspace, "task", other)) != otherBefore {
		t.Fatal("comment grant invalidation did not target its task")
	}
	a.Observe(events.Event{Type: "permission:updated"})
	if version(permissionResourceScope(workspace, "task", other)) == otherBefore {
		t.Fatal("unknown-scope permission event retained cache")
	}
}
