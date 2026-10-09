package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

// 2026-10-09 coder(lq): Verify real router ordering: cached permission reads
// still require authenticated workspace membership, while task detail content
// remains live even when the same user's permission response is already warm.
func TestPermissionReadCacheRouterMembershipAndLiveDetail(t *testing.T) {
	t.Setenv("PROJECT_PERMISSION_ROLLOUT_PHASE", "restricted")
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	task := fx.Issue(t, "Permission cache route task")
	viewer := fx.User(t, "Permission cache viewer", "permission-cache-route-viewer@example.test")
	fx.Member(t, testWorkspaceID, viewer, "member")
	fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": task, "subject_type": "user", "subject_id": viewer, "role_key": "viewer"})
	token, err := generateTestJWT(viewer, "permission-cache-route-viewer@example.test", "Permission cache viewer")
	if err != nil {
		t.Fatal(err)
	}
	metrics := obsmetrics.NewReadCacheMetrics()
	hub := realtime.NewHub()
	router, _ := NewRouterWithOptions(testPool, hub, events.New(), analytics.NoopClient{}, nil, RouterOptions{ReadCacheMetrics: metrics})
	read := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	permissionPath := "/api/issues/" + task + "/effective-access"
	for range 2 {
		if got := read(permissionPath); got.Code != 200 {
			t.Fatalf("permission route status=%d body=%s", got.Code, got.Body.String())
		}
	}
	if got := promtest.ToFloat64(metrics.Requests.WithLabelValues("permission", "effective_access", "hit")); got != 1 {
		t.Fatalf("router cache hit=%v want 1", got)
	}
	fx.Exec(t, "UPDATE issue SET title='Live updated title' WHERE id=$1", task)
	detail := read("/api/issues/" + task)
	var content struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if detail.Code != 200 || content.Title != "Live updated title" {
		t.Fatalf("detail content was cached: status=%d title=%q", detail.Code, content.Title)
	}
	fx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, viewer)
	if got := read(permissionPath); got.Code == 200 {
		t.Fatal("cached permission bypassed router membership gate")
	}
	if got := promtest.ToFloat64(metrics.Requests.WithLabelValues("permission", "effective_access", "hit")); got != 1 {
		t.Fatal("former member reached the permission cache")
	}
}

// 2026-10-09 coder(lq): Revoke through the actual API after warming every
// task permission response; cached manager access must never authorize writes.
func TestPermissionReadCacheRouterRevocationAndLiveWrites(t *testing.T) {
	t.Setenv("PROJECT_PERMISSION_ROLLOUT_PHASE", "restricted")
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	task := fx.Issue(t, "Permission cache revoke task")
	manager := fx.User(t, "Cached manager", "permission-cache-manager@example.test")
	recipient := fx.User(t, "Grant recipient", "permission-cache-recipient@example.test")
	fx.Member(t, testWorkspaceID, manager, "member")
	fx.Member(t, testWorkspaceID, recipient, "member")
	fx.Insert(t, "projectauth_issue_access_grants", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": task, "subject_type": "user", "subject_id": manager, "role_key": "manager"})
	metrics := obsmetrics.NewReadCacheMetrics()
	router, _ := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{ReadCacheMetrics: metrics})
	managerToken, err := generateTestJWT(manager, "permission-cache-manager@example.test", "Cached manager")
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := generateTestJWT(testUserID, "owner@example.test", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	request := func(token, method, suffix string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "/api/issues/"+task+suffix, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	grant := map[string]string{"subject_type": "user", "subject_id": recipient, "role": "viewer"}
	if got := request(managerToken, http.MethodPost, "/access-grants", grant); got.Code != http.StatusCreated {
		t.Fatalf("manager could not grant before revoke: %d %s", got.Code, got.Body.String())
	}
	for _, endpoint := range []string{"effective-access", "access-control", "access-grants"} {
		for range 2 {
			if got := request(managerToken, http.MethodGet, "/"+endpoint, nil); got.Code != http.StatusOK {
				t.Fatalf("warm %s: %d %s", endpoint, got.Code, got.Body.String())
			}
		}
	}
	for _, endpoint := range []string{"effective_access", "access_control", "issue_grants"} {
		if got := promtest.ToFloat64(metrics.Requests.WithLabelValues("permission", endpoint, "hit")); got != 1 {
			t.Fatalf("%s did not hit before revoke: %v", endpoint, got)
		}
	}
	revoke := map[string]string{"subject_type": "user", "subject_id": manager, "role": "manager"}
	if got := request(ownerToken, http.MethodDelete, "/access-grants", revoke); got.Code != http.StatusNoContent {
		t.Fatalf("owner revoke failed: %d %s", got.Code, got.Body.String())
	}
	for _, endpoint := range []string{"effective-access", "access-control", "access-grants"} {
		if got := request(managerToken, http.MethodGet, "/"+endpoint, nil); got.Code != http.StatusForbidden {
			t.Fatalf("revoked manager retained %s: %d %s", endpoint, got.Code, got.Body.String())
		}
	}
	grant["role"] = "manager"
	if got := request(managerToken, http.MethodPost, "/access-grants", grant); got.Code != http.StatusForbidden {
		t.Fatalf("cached manager permission authorized write: %d %s", got.Code, got.Body.String())
	}
	var role string
	fx.QueryRow(t, "SELECT role_key FROM projectauth_issue_access_grants WHERE workspace_id=$1 AND issue_id=$2 AND subject_id=$3", testWorkspaceID, task, recipient).Scan(&role)
	if role != "viewer" {
		t.Fatalf("denied write changed recipient role to %q", role)
	}
}
