package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestListOrganizationMembersMarksRealLoginsOnly(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}

	importedUserID := dbfx.User(t, "Imported Directory User", "dir-imported@multica.ai")
	loggedInUserID := dbfx.User(t, "Logged In Directory User", "dir-logged-in@multica.ai")
	dbfx.Member(t, testWorkspaceID, importedUserID, "member")
	dbfx.Member(t, testWorkspaceID, loggedInUserID, "member")

	organizationID := dbfx.Insert(t, "projectauth_organizations", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"provider":     "file",
		"external_id":  "dir-login-status",
		"name":         "Directory Login Status",
		"status":       "active",
	})
	dbfx.InsertNoID(t, "projectauth_organization_members", testutil.Cols{
		"organization_id": organizationID,
		"workspace_id":    testWorkspaceID,
		"user_id":         importedUserID,
	}, "organization_id = $1 AND user_id = $2", organizationID, importedUserID)
	dbfx.InsertNoID(t, "projectauth_organization_members", testutil.Cols{
		"organization_id": organizationID,
		"workspace_id":    testWorkspaceID,
		"user_id":         loggedInUserID,
	}, "organization_id = $1 AND user_id = $2", organizationID, loggedInUserID)
	dbfx.InsertNoID(t, "projectauth_user_logins", testutil.Cols{
		"user_id":           loggedInUserID,
		"last_logged_in_at": testutil.Raw("now()"),
	}, "user_id = $1", loggedInUserID)

	members, err := (&projectAuthRepository{db: testPool}).ListOrganizationMembers(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("ListOrganizationMembers: %v", err)
	}

	got := map[string]bool{}
	for _, member := range members {
		if member.OrganizationID == organizationID {
			got[member.UserID] = member.HasLoggedIn
		}
	}
	if got[importedUserID] {
		t.Fatalf("imported user %s marked as logged in", importedUserID)
	}
	if !got[loggedInUserID] {
		t.Fatalf("logged-in user %s not marked as logged in: %#v", loggedInUserID, got)
	}
}
