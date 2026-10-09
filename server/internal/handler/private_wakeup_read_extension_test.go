package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-10 coder(lq): Hidden wakeups cannot occupy pages, inflate counts,
// appear in search/paused cues, or grant management to a task viewer.
func TestWorkspaceWakeupsPrivateACLBeforePaginationAndCounts(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newIssueTableVisibilityFixture(t)
	h := permissionCacheHandler(t)
	rt := f.fx.Runtime(t, "Wakeup private runtime")
	agent := f.fx.Agent(t, "Wakeup private agent", rt)
	var visible string
	f.fx.QueryRow(t, "SELECT id::text FROM issue WHERE workspace_id=$1 AND title='Shared todo'", f.ws).Scan(&visible)
	hidden := f.fx.Issue(t, "Secret wakeup parent", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	f.fx.Exec(t, "UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1", visible, agent)
	f.fx.Issue(t, "Visible open child", testutil.Cols{"parent_issue_id": visible})
	f.fx.Issue(t, "Archived barrier child", testutil.Cols{"parent_issue_id": visible, "archived_at": testutil.Raw("now()")})
	f.fx.Issue(t, "Hidden open child", testutil.Cols{"parent_issue_id": hidden})
	insert := func(issue string, system, paused bool, at time.Time) string {
		cols := testutil.Cols{"id": uuid.NewString(), "workspace_id": f.ws, "issue_id": issue, "instruction": "check", "kind": "event", "mode": "continuous", "event_types": testutil.Raw("ARRAY['comment.created']"), "created_at": at}
		if system {
			cols["system_rule"] = "child_done"
		} else {
			cols["agent_id"] = agent
			cols["created_by"] = testUserID
		}
		if paused {
			cols["enabled"] = false
			cols["paused_reason"] = "rate"
		}
		return f.fx.Insert(t, "issue_wakeup", cols)
	}
	now := time.Now().UTC()
	normal := insert(visible, false, false, now.Add(-time.Hour))
	system := insert(visible, true, false, now.Add(-time.Minute))
	insert(hidden, false, false, now)
	insert(hidden, true, false, now.Add(time.Minute))
	insert(hidden, false, true, now.Add(2*time.Minute))
	paused := insert(visible, false, true, now.Add(-2*time.Hour))
	archived := f.fx.Issue(t, "Archived parent wakeup", testutil.Cols{"archived_at": testutil.Raw("now()")})
	insert(archived, false, false, now)
	read := func(user, path string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		req := newRequestAs(user, http.MethodGet, path, nil)
		req.Header.Set("X-Workspace-ID", f.ws)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		return rec
	}
	type item struct {
		ID        string `json:"id"`
		CanManage bool   `json:"can_manage"`
		Remaining int    `json:"system_remaining"`
	}
	type page struct {
		Items  []item         `json:"items"`
		Total  int            `json:"total"`
		Counts map[string]int `json:"counts"`
	}
	get := func(user, path string) page {
		rec := read(user, path, h.ListWorkspaceWakeups)
		var out page
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := get(f.reader, "/?scope=all&limit=1")
	if first.Total != 3 || first.Counts["all"] != 3 || len(first.Items) != 1 || first.Items[0].ID != system || first.Items[0].CanManage || first.Items[0].Remaining != 1 {
		t.Fatalf("private first page/barrier: %+v", first)
	}
	second := get(f.reader, "/?scope=all&limit=1&offset=1")
	if second.Total != 3 || len(second.Items) != 1 || second.Items[0].ID != normal {
		t.Fatalf("hidden entries consumed page: %+v", second)
	}
	if empty := get(f.reader, "/?scope=all&offset=100"); empty.Total != 3 || len(empty.Items) != 0 {
		t.Fatalf("empty-offset total: %+v", empty)
	}
	if found := get(f.reader, "/?scope=all&search=Secret"); found.Total != 0 || found.Counts["all"] != 3 {
		t.Fatalf("hidden search/count leak: %+v", found)
	}
	for _, tc := range []struct {
		path string
		fn   http.HandlerFunc
		want string
	}{{"/summaries", h.ListWorkspaceWakeupSummaries, normal}, {"/paused", h.ListPausedWakeups, paused}} {
		body := read(f.reader, tc.path, tc.fn).Body.String()
		if strings.Contains(body, hidden) || strings.Contains(body, archived) || !strings.Contains(body, tc.want) {
			t.Fatalf("%s ACL/archive: %s", tc.path, body)
		}
	}
	if owner := get(testUserID, "/?scope=all"); owner.Total != 6 {
		t.Fatalf("owner bypass: %+v", owner)
	}
	if owner := get(testUserID, "/?scope=all&include_workspace_owned=false"); owner.Total != 6 {
		t.Fatalf("creator rights with owner bypass disabled: %+v", owner)
	}
	ownerOnly := f.fx.User(t, "Wakeup owner only", "wakeup-owner@example.test")
	f.fx.Member(t, f.ws, ownerOnly, "owner")
	if out := get(ownerOnly, "/?scope=all"); out.Total != 6 {
		t.Fatalf("owner-only bypass: %+v", out)
	}
	if out := get(ownerOnly, "/?scope=all&include_workspace_owned=false"); out.Total != 0 {
		t.Fatalf("owner toggle ignored: %+v", out)
	}
	for _, phase := range []projectauth.RolloutPhase{projectauth.RolloutOff, projectauth.RolloutShadow, projectauth.RolloutReader} {
		h.ProjectAuth = projectauth.NewWithRollout(&projectAuthRepository{db: testPool}, phase)
		want := 6
		if phase == projectauth.RolloutReader {
			want = 3
		}
		if out := get(f.reader, "/?scope=all"); out.Total != want {
			t.Fatalf("%s: %+v", phase, out)
		}
	}
	h.ProjectAuth = projectauth.NewWithRollout(&projectAuthRepository{db: testPool}, projectauth.RolloutRestricted)
	var project string
	f.fx.QueryRow(t, "SELECT project_id::text FROM issue WHERE id=$1", visible).Scan(&project)
	if err := upsertIssueAccessGrant(context.Background(), testPool, visible, project, f.reader, projectauth.TaskMember); err != nil {
		t.Fatal(err)
	}
	f.fx.Cleanup(t, "DELETE FROM projectauth_access_grants WHERE issue_id=$1 AND subject_id=$2", visible, f.reader)
	task := f.fx.Task(t, agent, testutil.Cols{"issue_id": visible, "runtime_id": rt, "originator_user_id": f.reader, "accountable_user_id": f.reader, "status": "running", "started_at": testutil.Raw("now()")})
	delegated := func() page {
		req := newRequestAs(testUserID, http.MethodGet, "/?scope=all", nil)
		req.Header.Set("X-Workspace-ID", f.ws)
		req.Header.Set("X-Agent-ID", agent)
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Task-ID", task)
		rec := httptest.NewRecorder()
		h.ListWorkspaceWakeups(rec, req)
		var out page
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("delegated status: %d %s", rec.Code, rec.Body.String())
		}
		return out
	}
	if out := delegated(); out.Total != 3 {
		t.Fatalf("delegation borrowed owner scope: %+v", out)
	}
	f.fx.Exec(t, "UPDATE projectauth_access_grants SET role_key='viewer' WHERE issue_id=$1 AND subject_id=$2", visible, f.reader)
	if out := delegated(); out.Total != 0 {
		t.Fatalf("source AgentUse revoke ignored: %+v", out)
	}
	// A machine principal without validated delegation cannot borrow the owner.
	req := newRequestAs(testUserID, http.MethodGet, "/?scope=all", nil)
	req.Header.Set("X-Workspace-ID", f.ws)
	req.Header.Set("X-Agent-ID", agent)
	req.Header.Set("X-Actor-Source", "task_token")
	rec := httptest.NewRecorder()
	h.ListWorkspaceWakeups(rec, req)
	var denied page
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &denied) != nil || denied.Total != 0 {
		t.Fatalf("machine borrowed owner: %d %s", rec.Code, rec.Body.String())
	}
}
