package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-08-27 coder(lq): Guard the two workspace-wide list surfaces together.
// Historical task grants must not bypass the project-level visibility boundary.
func TestProjectAndIssueListsRespectCurrentUserPermissions(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	memberID := createSecondWorkspaceMember(t)
	adminID := createPlainMember(t, "project-list-admin")
	if _, err := testPool.Exec(ctx, `UPDATE member SET role = 'admin' WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, adminID); err != nil {
		t.Fatalf("promote project list admin: %v", err)
	}
	projectIDs := make([]string, 2)
	issueIDs := make([]string, 2)
	projectlessIssueIDs := make([]string, 2)
	for i := range projectIDs {
		if err := testPool.QueryRow(ctx, `
			INSERT INTO project (workspace_id, title)
			VALUES ($1, $2)
			RETURNING id
		`, testWorkspaceID, fmt.Sprintf("Permission list project %d", i)).Scan(&projectIDs[i]); err != nil {
			t.Fatalf("create project %d: %v", i, err)
		}
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, project_id, title, status, priority,
				creator_type, creator_id, number, position
			)
			VALUES (
				$1, $2, $3, 'todo', 'none', 'member', $4,
				(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
				100
			)
			RETURNING id
		`, testWorkspaceID, projectIDs[i], fmt.Sprintf("Permission list issue %d", i), testUserID).Scan(&issueIDs[i]); err != nil {
			t.Fatalf("create issue %d: %v", i, err)
		}
	}
	for i, values := range []struct {
		creatorType string
		creatorID   string
		assigneeID  *string
	}{
		{creatorType: "member", creatorID: memberID},
		{creatorType: "member", creatorID: testUserID, assigneeID: &memberID},
	} {
		var assigneeType any
		var assigneeID any
		if values.assigneeID != nil {
			assigneeType = "member"
			assigneeID = *values.assigneeID
		}
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, title, status, priority,
				creator_type, creator_id, assignee_type, assignee_id,
				number, position
			)
			VALUES ($1, $2, 'todo', 'none', $3, $4, $5, $6,
				(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
				100)
			RETURNING id
		`, testWorkspaceID, fmt.Sprintf("Projectless permission issue %d", i), values.creatorType, values.creatorID, assigneeType, assigneeID).Scan(&projectlessIssueIDs[i]); err != nil {
			t.Fatalf("create projectless issue %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM project WHERE id = ANY($1::uuid[])`, projectIDs)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = ANY($1::uuid[])`, projectlessIssueIDs)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role)
		VALUES ($1, $2, 'viewer')
	`, projectIDs[0], memberID); err != nil {
		t.Fatalf("grant visible project: %v", err)
	}

	previous := testHandler.ProjectAuth
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })

	projectIDsFor := func(t *testing.T, userID string) map[string]bool {
		t.Helper()
		recorder := httptest.NewRecorder()
		testHandler.ListProjects(recorder, newRequestAs(userID, http.MethodGet,
			"/api/projects?workspace_id="+testWorkspaceID, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("ListProjects: got %d: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Projects []ProjectResponse `json:"projects"`
		}
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode projects: %v", err)
		}
		result := make(map[string]bool, len(response.Projects))
		for _, project := range response.Projects {
			result[project.ID] = true
		}
		return result
	}

	issueIDsFor := func(t *testing.T, userID string) map[string]bool {
		t.Helper()
		recorder := httptest.NewRecorder()
		testHandler.ListIssues(recorder, newRequestAs(userID, http.MethodGet,
			"/api/issues?workspace_id="+testWorkspaceID+"&limit=100", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("ListIssues: got %d: %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Issues []IssueResponse `json:"issues"`
		}
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode issues: %v", err)
		}
		result := make(map[string]bool, len(response.Issues))
		for _, issue := range response.Issues {
			result[issue.ID] = true
		}
		return result
	}

	memberProjects := projectIDsFor(t, memberID)
	if !memberProjects[projectIDs[0]] || memberProjects[projectIDs[1]] {
		t.Fatalf("member projects = %v; want only %s", memberProjects, projectIDs[0])
	}
	memberIssues := issueIDsFor(t, memberID)
	if !memberIssues[issueIDs[0]] || memberIssues[issueIDs[1]] {
		t.Fatalf("member issues = %v; want only %s", memberIssues, issueIDs[0])
	}
	for _, issueID := range projectlessIssueIDs {
		if !memberIssues[issueID] {
			t.Fatalf("member cannot see projectless issue %s; issues = %v", issueID, memberIssues)
		}
	}

	adminProjects := projectIDsFor(t, adminID)
	if len(adminProjects) != 0 {
		t.Fatalf("workspace admin without project grant can see projects = %v; want none", adminProjects)
	}
	adminIssues := issueIDsFor(t, adminID)
	if len(adminIssues) != 0 {
		t.Fatalf("workspace admin without project grant can see issues = %v; want none", adminIssues)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role)
		VALUES ($1, $2, 'viewer')
	`, projectIDs[1], adminID); err != nil {
		t.Fatalf("grant admin visible project: %v", err)
	}
	adminProjects = projectIDsFor(t, adminID)
	if !adminProjects[projectIDs[1]] || adminProjects[projectIDs[0]] {
		t.Fatalf("workspace admin projects after explicit grant = %v; want only %s", adminProjects, projectIDs[1])
	}
	adminIssues = issueIDsFor(t, adminID)
	if !adminIssues[issueIDs[1]] || adminIssues[issueIDs[0]] {
		t.Fatalf("workspace admin issues after explicit grant = %v; want only %s", adminIssues, issueIDs[1])
	}

	ownerProjects := projectIDsFor(t, testUserID)
	ownerIssues := issueIDsFor(t, testUserID)
	for i := range projectIDs {
		if !ownerProjects[projectIDs[i]] || !ownerIssues[issueIDs[i]] {
			t.Fatalf("workspace owner missing project/issue %d", i)
		}
	}
}
