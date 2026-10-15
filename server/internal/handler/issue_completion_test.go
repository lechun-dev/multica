package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func missionCompletionActivity(t *testing.T, issueID, from, to string, at time.Time) {
	t.Helper()
	details, err := json.Marshal(map[string]string{"from": from, "to": to})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID,
		"actor_type": "member", "actor_id": testUserID,
		"action": "status_changed", "details": details, "created_at": at,
	})
}

func TestMissionIssueCompletionHistory(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "true")
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	custom := "mission_completed"
	dbfx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": testWorkspaceID, "key": custom, "name": "Mission completed",
		"category": "done", "color": "#123456",
	})
	for _, tc := range []struct {
		name, status string
		transitions  [][2]string
		wantIndex    int
	}{
		{"first completion", "done", [][2]string{{"todo", "done"}}, 1},
		{"recompleted", "done", [][2]string{{"todo", "done"}, {"done", "todo"}, {"todo", "done"}}, 3},
		{"done to done", custom, [][2]string{{"todo", "done"}, {"done", custom}}, 1},
		{"custom done", custom, [][2]string{{"in_progress", custom}}, 1},
		{"reopened", "todo", [][2]string{{"todo", "done"}, {"done", "todo"}}, 0},
		{"cancelled", "cancelled", [][2]string{{"todo", "done"}, {"done", "cancelled"}}, 0},
		{"missing history", "done", nil, 0},
		{"unknown origin", "done", [][2]string{{"", "done"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := dbfx.Issue(t, "Mission completion "+tc.name, testutil.Cols{"status": tc.status})
			for i, transition := range tc.transitions {
				missionCompletionActivity(t, id, transition[0], transition[1], base.Add(time.Duration(i+1)*time.Hour))
			}
			req := testutil.WithURLParams(newRequest(http.MethodGet, "/api/issues/"+id+"?include_completed_at=true", nil), "id", id)
			out := testutil.Call(t, testHandler.GetIssue, req).Want(http.StatusOK).Map()
			got, present := out["completed_at"]
			if !present {
				t.Fatal("opt-in completion field absent")
			}
			if tc.wantIndex == 0 {
				if got != nil {
					t.Fatalf("completed_at = %v, want null", got)
				}
			} else if want := base.Add(time.Duration(tc.wantIndex) * time.Hour).Format(time.RFC3339Nano); got != want {
				t.Fatalf("completed_at = %v, want %s", got, want)
			}
			plain := testutil.WithURLParams(newRequest(http.MethodGet, "/api/issues/"+id, nil), "id", id)
			if _, present := testutil.Call(t, testHandler.GetIssue, plain).Want(http.StatusOK).Map()["completed_at"]; present {
				t.Fatal("legacy response unexpectedly gained completion field")
			}
		})
	}
}

func TestMissionIssueTimeRangesBeforePagination(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "true")
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	projectID := dbfx.Project(t, "Mission range isolation")
	var ids []string
	for i := 0; i < 3; i++ {
		id := dbfx.Issue(t, fmt.Sprintf("Mission range %d", i), testutil.Cols{
			"project_id": projectID, "status": "done", "created_at": base.Add(time.Duration(i) * time.Hour),
		})
		missionCompletionActivity(t, id, "todo", "done", base.Add(time.Duration(24+i)*time.Hour))
		ids = append(ids, id)
	}
	list := func(params url.Values) struct {
		Issues []map[string]any `json:"issues"`
		Total  int              `json:"total"`
	} {
		t.Helper()
		params.Set("workspace_id", testWorkspaceID)
		params.Set("project_id", projectID)
		params.Set("include_completed_at", "true")
		var out struct {
			Issues []map[string]any `json:"issues"`
			Total  int              `json:"total"`
		}
		testutil.Call(t, testHandler.ListIssues, newRequest(http.MethodGet, "/api/issues?"+params.Encode(), nil)).Want(http.StatusOK).JSON(&out)
		return out
	}
	params := url.Values{
		"created_from":   {base.Add(time.Hour).Format(time.RFC3339)},
		"created_to":     {base.Add(3 * time.Hour).Format(time.RFC3339)},
		"completed_from": {base.Add(25 * time.Hour).Format(time.RFC3339)},
		"completed_to":   {base.Add(27 * time.Hour).Format(time.RFC3339)},
		"sort":           {"completed_at"}, "direction": {"asc"}, "limit": {"1"},
	}
	out := list(params)
	if out.Total != 2 || len(out.Issues) != 1 || out.Issues[0]["id"] != ids[1] {
		t.Fatalf("first authorized/filtered page = %+v", out)
	}
	params.Set("offset", "1")
	out = list(params)
	if out.Total != 2 || len(out.Issues) != 1 || out.Issues[0]["id"] != ids[2] {
		t.Fatalf("second authorized/filtered page = %+v", out)
	}
	params.Set("offset", "0")
	params.Set("completed_to", base.Add(26*time.Hour).Format(time.RFC3339))
	out = list(params)
	if out.Total != 1 || len(out.Issues) != 1 || out.Issues[0]["id"] != ids[1] {
		t.Fatalf("exclusive upper boundary = %+v", out)
	}
	for _, invalid := range []url.Values{
		{"created_from": {"2026-02-30T00:00:00Z"}},
		{"completed_to": {"2026-10-14"}},
		{"created_from": {"2026-10-14T00:00:00Z"}, "created_to": {"2026-10-14T00:00:00Z"}},
		{"open_only": {"true"}, "include_completed_at": {"true"}},
	} {
		invalid.Set("workspace_id", testWorkspaceID)
		testutil.Call(t, testHandler.ListIssues, newRequest(http.MethodGet, "/api/issues?"+invalid.Encode(), nil)).Want(http.StatusBadRequest)
	}
}

func TestMissionCompletionReadsPreserveWorkspaceACL(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "true")
	member := createPlainMember(t, "mission-task-reader")
	creator := createPlainMember(t, "mission-task-creator")
	previous := testHandler.ProjectAuth
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })
	visible := dbfx.Project(t, "Mission visible project")
	hidden := dbfx.Project(t, "Mission hidden project")
	repository := &projectAuthRepository{db: testPool}
	if err := repository.AddProjectMember(context.Background(), visible, member, projectauth.ProjectViewer); err != nil {
		t.Fatal(err)
	}
	visibleID := dbfx.Issue(t, "Mission ACL visible", testutil.Cols{"project_id": visible, "creator_id": creator, "status": "done"})
	hiddenID := dbfx.Issue(t, "Mission ACL hidden", testutil.Cols{"project_id": hidden, "creator_id": creator, "status": "done"})
	for _, id := range []string{visibleID, hiddenID} {
		missionCompletionActivity(t, id, "todo", "done", time.Now().Add(-time.Hour).UTC())
	}
	for _, tc := range []struct {
		user   string
		want   int
		hidden int
	}{
		{member, 1, http.StatusForbidden},
		{testUserID, 2, http.StatusOK},
	} {
		var out struct {
			Issues []map[string]any `json:"issues"`
			Total  int              `json:"total"`
		}
		path := "/api/issues?workspace_id=" + testWorkspaceID + "&q=Mission+ACL&limit=1&include_completed_at=true&completed_from=2020-01-01T00:00:00Z"
		testutil.Call(t, testHandler.ListIssues, newRequestAs(tc.user, http.MethodGet, path, nil)).Want(http.StatusOK).JSON(&out)
		if out.Total != tc.want || len(out.Issues) != 1 {
			t.Fatalf("user %s authorized total/page = %+v", tc.user, out)
		}
		if tc.user == member && out.Issues[0]["id"] != visibleID {
			t.Fatalf("member received hidden task: %+v", out)
		}
		for _, detail := range []struct {
			id     string
			status int
		}{{visibleID, http.StatusOK}, {hiddenID, tc.hidden}} {
			req := testutil.WithURLParams(newRequestAs(tc.user, http.MethodGet, "/api/issues/"+detail.id+"?include_completed_at=true", nil), "id", detail.id)
			testutil.Call(t, testHandler.GetIssue, req).Want(detail.status)
		}
	}
}
