package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-10-10 coder(lq): Check displayed effective permissions against real
// child creation for a non-assignee, including the sixth child and ACL edges.
func TestCreateChildInheritsProjectCreationPermission(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name, role, subjectType string
		wantAllowed             bool
	}{
		{"owner", "owner", "user", true},
		{"manager", "manager", "user", true},
		{"member", "member", "user", true},
		{"organization-member", "member", "organization", true},
		{"viewer", "viewer", "user", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := dbfx.Workspace(t, "Child permission "+tc.name, "child-permission-"+tc.name)
			fx := testutil.New(testPool, ws, testUserID)
			user := dbfx.User(t, "Child creator "+tc.name, "child-creator-"+tc.name+"@example.test")
			fx.Member(t, ws, user, "member")
			fx.Member(t, ws, testUserID, "member")
			project := fx.Project(t, "Parent project")
			otherProject := fx.Project(t, "Unauthorized target")
			parent := fx.Issue(t, "Parent owned by someone else", testutil.Cols{"project_id": project, "assignee_type": "member", "assignee_id": testUserID})
			// 2026-10-10 coder(lq): Fixtures insert issues without advancing the
			// counter; align it before exercising the real API number allocator.
			fx.Exec(t, "UPDATE workspace SET issue_counter=(SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id=$1) WHERE id=$1", ws)
			subjectID := user
			if tc.subjectType == "organization" {
				subjectID = fx.Insert(t, "projectauth_organizations", testutil.Cols{"workspace_id": ws, "provider": "test", "external_id": "child-create"})
				fx.InsertNoID(t, "projectauth_organization_members", testutil.Cols{"workspace_id": ws, "organization_id": subjectID, "user_id": user}, "workspace_id=$1 AND user_id=$2", ws, user)
			}
			grant := fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": ws, "project_id": project, "subject_type": tc.subjectType, "subject_id": subjectID, "role_key": tc.role})
			fx.Cleanup(t, "DELETE FROM projectauth_access_grants WHERE workspace_id=$1", ws)
			h := permissionCacheHandler(t)
			read := httptest.NewRecorder()
			h.GetIssueEffectiveAccess(read, permissionCacheRequest(user, ws, parent, "effective-access"))
			if read.Code != http.StatusOK {
				t.Fatalf("effective permissions: %d %s", read.Code, read.Body.String())
			}
			var access projectauth.EffectiveIssueAccess
			if err := json.Unmarshal(read.Body.Bytes(), &access); err != nil {
				t.Fatal(err)
			}
			hasChildCreate := false
			for _, permission := range access.Permissions {
				if permission == projectauth.IssueChildCreate {
					hasChildCreate = true
				}
			}
			if hasChildCreate != tc.wantAllowed {
				t.Fatalf("displayed child creation=%v, want %v", hasChildCreate, tc.wantAllowed)
			}
			attempt := 0
			create := func(target string, want int) {
				t.Helper()
				attempt++
				body := map[string]any{"title": fmt.Sprintf("Child attempt %d", attempt), "parent_issue_id": parent, "project_id": target}
				req := newRequestAs(user, http.MethodPost, "/api/issues", body)
				req.Header.Set("X-Workspace-ID", ws)
				response := httptest.NewRecorder()
				h.CreateIssue(response, req)
				if response.Code != want {
					t.Fatalf("create attempt %d: %d %s, want %d", attempt, response.Code, response.Body.String(), want)
				}
				if want == http.StatusCreated {
					var child IssueResponse
					if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
						t.Fatal(err)
					}
					if child.ParentIssueID == nil || *child.ParentIssueID != parent || child.ProjectID == nil || *child.ProjectID != target {
						t.Fatalf("wrong child relationship: %#v", child)
					}
					fx.Cleanup(t, "DELETE FROM issue_permissions WHERE issue_id=$1", child.ID)
					fx.Cleanup(t, "DELETE FROM issue WHERE id=$1", child.ID)
				}
			}
			if !tc.wantAllowed {
				create(project, http.StatusForbidden)
				if count := fx.Count(t, "SELECT count(*) FROM issue WHERE parent_issue_id=$1", parent); count != 0 {
					t.Fatalf("denied create persisted %d children", count)
				}
				return
			}
			for range 12 {
				create(project, http.StatusCreated)
			}
			if count := fx.Count(t, "SELECT count(*) FROM issue WHERE parent_issue_id=$1", parent); count != 12 {
				t.Fatalf("created children=%d, want 12", count)
			}
			create(otherProject, http.StatusForbidden)
			fx.InsertNoID(t, "projectauth_issue_policies", testutil.Cols{"workspace_id": ws, "issue_id": parent, "project_access_mode": "restricted"}, "issue_id=$1", parent)
			create(project, http.StatusForbidden)
			fx.Exec(t, "UPDATE issue SET assignee_id=$2 WHERE id=$1", parent, user)
			create(project, http.StatusCreated)
			fx.Exec(t, "UPDATE issue SET assignee_id=$2 WHERE id=$1", parent, testUserID)
			taskGrant := fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": ws, "project_id": project, "issue_id": parent, "subject_type": "user", "subject_id": user, "role_key": "member"})
			create(project, http.StatusCreated)
			fx.Exec(t, "DELETE FROM projectauth_access_grants WHERE id=$1", taskGrant)
			fx.Exec(t, "UPDATE projectauth_issue_policies SET project_access_mode='inherit' WHERE issue_id=$1", parent)
			fx.Exec(t, "DELETE FROM projectauth_access_grants WHERE id=$1", grant)
			create(project, http.StatusForbidden)
			if count := fx.Count(t, "SELECT count(*) FROM issue WHERE parent_issue_id=$1", parent); count != 14 {
				t.Fatalf("denied create changed children=%d, want 14", count)
			}
		})
	}
}
