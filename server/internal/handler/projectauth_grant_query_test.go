package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestListAccessGrantsOptionalIssuePreservesScope(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	projectID := dbfx.Project(t, "Optional issue grant query")
	issueID := dbfx.Issue(t, "Selected grant query task", testutil.Cols{"project_id": projectID})
	otherIssueID := dbfx.Issue(t, "Other grant query task", testutil.Cols{"project_id": projectID})
	bulkIssueID := dbfx.Issue(t, "Index-plan grant query task", testutil.Cols{"project_id": projectID})
	insertGrant := func(issue any) string {
		return dbfx.Insert(t, "projectauth_access_grants", testutil.Cols{
			"workspace_id": testWorkspaceID, "project_id": projectID, "issue_id": issue,
			"subject_type": "user", "subject_id": testUserID, "role_key": "member", "source": "manual",
		})
	}
	projectGrant := insertGrant(nil)
	taskGrant := insertGrant(issueID)
	otherTaskGrant := insertGrant(otherIssueID)
	cases := []struct {
		name, issueID string
		want          map[string]bool
	}{
		{"empty issue returns project grants only", "", map[string]bool{projectGrant: true}},
		{"selected issue includes its own and project grants", issueID, map[string]bool{projectGrant: true, taskGrant: true}},
		{"other issue does not leak selected task grants", otherIssueID, map[string]bool{projectGrant: true, otherTaskGrant: true}},
	}
	// 2026-10-09 coder(lq): Warm production connections can select a generic
	// plan that evaluates UUID index expressions before the empty-string guard.
	// Both plan modes must preserve the optional task filter without widening it.
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			// 2026-10-09 coder(lq): Session-local copies retain the real schema
			// and indexes while isolating the row distribution and planner stats.
			if _, err := tx.Exec(ctx, `
				CREATE TEMP TABLE projectauth_access_grants
				  (LIKE public.projectauth_access_grants INCLUDING ALL) ON COMMIT DROP`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO projectauth_access_grants
				SELECT * FROM public.projectauth_access_grants WHERE project_id=$1`, projectID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO projectauth_access_grants (workspace_id,project_id,issue_id,subject_type,subject_id,role_key,source)
				SELECT $2::uuid,$1::uuid,$3::uuid,'user',gen_random_uuid()::text,'member','manual' FROM generate_series(1,512)
			`, projectID, testWorkspaceID, bulkIssueID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `ANALYZE projectauth_access_grants`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('plan_cache_mode', $1, true)`, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
				t.Fatal(err)
			}
			repo := &projectAuthRepository{db: tx}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					grants, err := repo.ListAccessGrants(ctx, testWorkspaceID, projectID, tc.issueID)
					if err != nil {
						t.Fatalf("ListAccessGrants(%q): %v", tc.issueID, err)
					}
					got := make(map[string]bool)
					for _, grant := range grants {
						if grant.Source == projectauth.GrantSourceManual {
							got[grant.ID] = true
						}
					}
					if len(got) != len(tc.want) {
						t.Fatalf("manual grants = %v, want %v", got, tc.want)
					}
					for id := range tc.want {
						if !got[id] {
							t.Fatalf("manual grant %s missing from %v", id, got)
						}
					}
				})
			}
			if _, err := repo.ListAccessGrants(ctx, testWorkspaceID, projectID, "invalid-uuid"); err == nil {
				t.Fatal("malformed nonempty issue IDs must not broaden access")
			}
		})
	}
}
