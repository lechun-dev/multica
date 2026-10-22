package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func missionAgentTestRequest(userID, scope, path string) *http.Request {
	r := newRequestAs(userID, http.MethodGet, path, nil)
	return r.WithContext(auth.WithMissionReadScopeForRequest(r.Context(), scope))
}

func TestMissionAgentMembersDirectory(t *testing.T) {
	name := fmt.Sprintf("李群-directory-%d", time.Now().UnixNano())
	var userIDs []string
	for i := 0; i < 2; i++ {
		id := dbfx.User(t, name, fmt.Sprintf("directory-%d-%d@example.test", i, time.Now().UnixNano()))
		dbfx.Member(t, testWorkspaceID, id, "member")
		userIDs = append(userIDs, id)
	}
	outside := dbfx.User(t, name, fmt.Sprintf("directory-outside-%d@example.test", time.Now().UnixNano()))
	workspace := dbfx.Workspace(t, "Other directory", fmt.Sprintf("directory-%d", time.Now().UnixNano()))
	dbfx.Member(t, workspace, outside, "owner")
	path := "/internal/agent/members?q=" + url.QueryEscape(name) + "&limit=1"
	var first, second struct {
		Members []map[string]any `json:"members"`
		Total   int              `json:"total"`
	}
	testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, path)).Want(http.StatusOK).JSON(&first)
	testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, path+"&offset=1")).Want(http.StatusOK).JSON(&second)
	if first.Total != 2 || second.Total != 2 || len(first.Members) != 1 || len(second.Members) != 1 {
		t.Fatalf("duplicate-name pages = %+v / %+v", first, second)
	}
	for i, row := range []map[string]any{first.Members[0], second.Members[0]} {
		if len(row) != 2 || row["name"] != name || (row["user_id"] != userIDs[0] && row["user_id"] != userIDs[1]) {
			t.Fatalf("page %d leaked fields, member.id, or another workspace: %+v", i, row)
		}
	}
	if first.Members[0]["user_id"].(string) >= second.Members[0]["user_id"].(string) {
		t.Fatal("directory pagination is not deterministic or repeated a user")
	}
	caseQuery := "/internal/agent/members?q=" + url.QueryEscape("  "+strings.ToUpper(name)+"  ")
	casePage := testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, caseQuery)).Want(http.StatusOK).Map()
	if casePage["total"] != float64(2) || len(casePage["members"].([]any)) != 2 {
		t.Fatalf("trimmed case-insensitive name search = %+v", casePage)
	}
	allPage := testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, "/internal/agent/members?limit=1")).Want(http.StatusOK).Map()
	if allPage["total"].(float64) < 2 || len(allPage["members"].([]any)) != 1 {
		t.Fatalf("unfiltered directory page = %+v", allPage)
	}
	for _, query := range []string{"q=not-present-directory", "q=" + url.QueryEscape(name) + "&offset=2147483647"} {
		out := testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, "/internal/agent/members?"+query)).Want(http.StatusOK).Map()
		if len(out["members"].([]any)) != 0 {
			t.Fatalf("empty page = %+v", out)
		}
	}
	for _, query := range []string{"limit=0", "limit=101", "offset=-1", "offset=2147483648", "q=", "q=a&q=b", "role=owner", "limit=1&limit=2", "q=" + strings.Repeat("a", 101)} {
		testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, "/internal/agent/members?"+query)).Want(http.StatusBadRequest)
	}
	testutil.Call(t, testHandler.ListAgentReadMembers, newRequest(http.MethodGet, path, nil)).Want(http.StatusForbidden)
	r := missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, "/internal/agent/members?workspace_id="+workspace)
	r.Header.Set("X-Workspace-ID", workspace)
	testutil.Call(t, testHandler.ListAgentReadMembers, r).Want(http.StatusNotFound)
	// A previously issued token cannot retain access after workspace removal.
	dbfx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, userIDs[0])
	testutil.Call(t, testHandler.ListAgentReadMembers, missionAgentTestRequest(userIDs[0], auth.MissionTaskReadScope, path)).Want(http.StatusNotFound)
}

func TestMissionAgentAssigneeNamesPreserveACL(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	previous := testHandler.ProjectAuth
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })
	reader := createPlainMember(t, "mission-directory-reader")
	assignee := createPlainMember(t, "mission-directory-assignee")
	name := "Mission Directory Assignee"
	dbfx.Exec(t, `UPDATE "user" SET name=$1 WHERE id=$2`, name, assignee)
	visible := dbfx.Project(t, "Directory visible project")
	hidden := dbfx.Project(t, "Directory hidden project")
	repository := &projectAuthRepository{db: testPool}
	if err := repository.AddProjectMember(context.Background(), visible, reader, projectauth.ProjectViewer); err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, "DELETE FROM project_members WHERE project_id=$1 AND user_id=$2", visible, reader)
	visibleID := dbfx.Issue(t, "Directory assigned visible", testutil.Cols{"project_id": visible, "creator_id": assignee, "assignee_type": "member", "assignee_id": assignee})
	hiddenID := dbfx.Issue(t, "Directory assigned hidden", testutil.Cols{"project_id": hidden, "creator_id": assignee, "assignee_type": "member", "assignee_id": assignee})
	for _, tc := range []struct {
		user, scope string
		wantTotal   int
		hiddenCode  int
	}{
		{reader, auth.MissionTaskReadScope, 1, http.StatusForbidden},
		{reader, auth.MissionOwnerReadScope, 1, http.StatusForbidden},
		{testUserID, auth.MissionOwnerReadScope, 2, http.StatusOK},
	} {
		path := "/internal/agent/issues?q=Directory+assigned&assignee_types=member&assignee_id=" + assignee + "&include_total=true&limit=1"
		var out struct {
			Issues []map[string]any `json:"issues"`
			Total  int              `json:"total"`
		}
		testutil.Call(t, testHandler.ListAgentReadIssues, missionAgentTestRequest(tc.user, tc.scope, path)).Want(http.StatusOK).JSON(&out)
		if out.Total != tc.wantTotal || len(out.Issues) != 1 || out.Issues[0]["assignee_name"] != name {
			t.Fatalf("caller %s page = %+v", tc.user, out)
		}
		if tc.user == reader && out.Issues[0]["id"] != visibleID {
			t.Fatal("assignee filtering bypassed caller ACL")
		}
		for _, detail := range []struct {
			id   string
			code int
		}{{visibleID, http.StatusOK}, {hiddenID, tc.hiddenCode}} {
			r := missionAgentTestRequest(tc.user, tc.scope, "/internal/agent/issues/"+detail.id)
			r = testutil.WithURLParams(r, "id", detail.id)
			response := testutil.Call(t, testHandler.GetAgentReadIssue, r).Want(detail.code)
			if detail.code == http.StatusOK && response.Map()["assignee_name"] != name {
				t.Fatal("detail omitted member display name")
			}
		}
	}
	combined := "/internal/agent/issues?project_id=" + visible + "&assignee_types=member&assignee_id=" + assignee + "&include_total=true"
	combinedOut := testutil.Call(t, testHandler.ListAgentReadIssues, missionAgentTestRequest(reader, auth.MissionTaskReadScope, combined)).Want(http.StatusOK).Map()
	if combinedOut["total"] != float64(1) || combinedOut["issues"].([]any)[0].(map[string]any)["id"] != visibleID {
		t.Fatalf("combined project and assignee filter = %+v", combinedOut)
	}
	plain := testutil.WithURLParams(newRequestAs(assignee, http.MethodGet, "/api/issues/"+visibleID, nil), "id", visibleID)
	if _, present := testutil.Call(t, testHandler.GetIssue, plain).Want(http.StatusOK).Map()["assignee_name"]; present {
		t.Fatal("ordinary Mission detail unexpectedly changed projection")
	}
}

func TestMissionAgentProjectsDirectory(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	previous := testHandler.ProjectAuth
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })
	reader := createPlainMember(t, "mission-project-reader")
	name := fmt.Sprintf("技术开发-directory-%d", time.Now().UnixNano())
	visible := dbfx.Project(t, name)
	completed := dbfx.Project(t, name, testutil.Cols{"status": "completed"})
	hidden := dbfx.Project(t, name)
	workspace := dbfx.Workspace(t, "Other projects", fmt.Sprintf("project-dir-%d", time.Now().UnixNano()))
	dbfx.Project(t, name, testutil.Cols{"workspace_id": workspace})
	repository := &projectAuthRepository{db: testPool}
	for _, id := range []string{visible, completed} {
		if err := repository.AddProjectMember(context.Background(), id, reader, projectauth.ProjectViewer); err != nil {
			t.Fatal(err)
		}
		dbfx.Cleanup(t, "DELETE FROM project_members WHERE project_id=$1 AND user_id=$2", id, reader)
		dbfx.Cleanup(t, "DELETE FROM projectauth_access_grants WHERE project_id=$1 AND subject_id=$2", id, reader)
	}
	path := "/internal/agent/projects?q=" + url.QueryEscape(name) + "&limit=1"
	read := func(user, scope, path string, total, count int) []map[string]any {
		t.Helper()
		var out struct {
			Projects []map[string]any `json:"projects"`
			Total    int              `json:"total"`
		}
		testutil.Call(t, testHandler.ListAgentReadProjects, missionAgentTestRequest(user, scope, path)).Want(http.StatusOK).JSON(&out)
		if out.Total != total || len(out.Projects) != count {
			t.Fatalf("project page: %+v; want total=%d length=%d", out, total, count)
		}
		for _, row := range out.Projects {
			if len(row) != 2 || row["title"] != name || (row["id"] != visible && row["id"] != completed && row["id"] != hidden) {
				t.Fatalf("unexpected fields or cross-workspace project: %+v", row)
			}
		}
		return out.Projects
	}
	first := read(reader, auth.MissionTaskReadScope, path, 2, 1)
	second := read(reader, auth.MissionTaskReadScope, path+"&offset=1", 2, 1)
	if first[0]["id"].(string) >= second[0]["id"].(string) || first[0]["id"] == hidden || second[0]["id"] == hidden {
		t.Fatal("paging leaked a hidden project or did not preserve UUID ordering")
	}
	// Empty projects and completed projects are mappings too, even without tasks.
	read(reader, auth.MissionTaskReadScope, "/internal/agent/projects?q="+url.QueryEscape("  "+strings.ToUpper(name)+"  "), 2, 2)
	read(reader, auth.MissionOwnerReadScope, path, 2, 1) // A scope alone cannot grant owner access.
	read(testUserID, auth.MissionOwnerReadScope, path, 3, 1)
	read(testUserID, auth.MissionTaskReadScope, path, 0, 0)
	read(reader, auth.MissionTaskReadScope, path+"&offset=2147483647", 2, 0)
	read(reader, auth.MissionTaskReadScope, "/internal/agent/projects?q=absent-project-directory", 0, 0)
	read(reader, auth.MissionTaskReadScope, "/internal/agent/projects", 2, 2)
	// Browser APIs must continue honoring the disabled owner bypass.
	var normal struct {
		Projects []map[string]any `json:"projects"`
	}
	testutil.Call(t, testHandler.ListProjects, newRequest(http.MethodGet, "/api/projects", nil)).Want(http.StatusOK).JSON(&normal)
	for _, project := range normal.Projects {
		if project["id"] == hidden {
			t.Fatal("internal owner bypass escaped into the normal project API")
		}
	}
	for _, query := range []string{"limit=0", "limit=101", "offset=-1", "offset=2147483648", "q=", "q=a&q=b", "status=completed", "limit=1&limit=2", "q=" + strings.Repeat("a", 101)} {
		testutil.Call(t, testHandler.ListAgentReadProjects, missionAgentTestRequest(reader, auth.MissionTaskReadScope, "/internal/agent/projects?"+query)).Want(http.StatusBadRequest)
	}
	testutil.Call(t, testHandler.ListAgentReadProjects, newRequest(http.MethodGet, path, nil)).Want(http.StatusForbidden)
	r := missionAgentTestRequest(reader, auth.MissionTaskReadScope, "/internal/agent/projects?workspace_id="+workspace)
	r.Header.Set("X-Workspace-ID", workspace)
	testutil.Call(t, testHandler.ListAgentReadProjects, r).Want(http.StatusNotFound)
	// Revoked grants are observed on the next request, not cached in the JWT.
	dbfx.Exec(t, "DELETE FROM project_members WHERE project_id=$1 AND user_id=$2", visible, reader)
	dbfx.Exec(t, "DELETE FROM projectauth_access_grants WHERE project_id=$1 AND subject_id=$2", visible, reader)
	read(reader, auth.MissionTaskReadScope, path, 1, 1)
	dbfx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, reader)
	testutil.Call(t, testHandler.ListAgentReadProjects, missionAgentTestRequest(reader, auth.MissionOwnerReadScope, path)).Want(http.StatusNotFound)
	// A token minted while the caller was owner cannot retain its bypass after demotion.
	dbfx.Member(t, workspace, reader, "owner")
	readOther := func(want int) {
		t.Helper()
		r := missionAgentTestRequest(reader, auth.MissionOwnerReadScope, "/internal/agent/projects")
		r.Header.Set("X-Workspace-ID", workspace)
		out := testutil.Call(t, testHandler.ListAgentReadProjects, r).Want(http.StatusOK).Map()
		if out["total"] != float64(want) {
			t.Fatalf("current owner role was not rechecked: %+v", out)
		}
	}
	readOther(1)
	dbfx.Exec(t, "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", workspace, reader)
	readOther(0)
}
