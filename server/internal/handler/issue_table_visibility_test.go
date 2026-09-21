package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// 2026-09-20 coder(lq): The table surfaces bind one materialized visibility set
// per statement instead of inlining the ACL predicate into WHERE. These tests
// keep the inline predicate as the reference so the rewrite cannot drift.
type issueTableVisibilityFixture struct {
	fx       *testutil.Fixture
	ws       string
	reader   string
	expected int
}

func newIssueTableVisibilityFixture(t *testing.T) issueTableVisibilityFixture {
	t.Helper()
	ws := dbfx.Workspace(t, "Table visibility", "table-visibility")
	reader := dbfx.User(t, "Table visibility reader", "table-visibility@example.test")
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, reader, "member")
	fx.Member(t, ws, testUserID, "owner")
	shared := fx.Project(t, "Shared project")
	hidden := fx.Project(t, "Hidden project")
	org := fx.Insert(t, "projectauth_organizations", testutil.Cols{"workspace_id": ws, "provider": "test", "external_id": "table-visibility"})
	fx.InsertNoID(t, "projectauth_organization_members", testutil.Cols{"workspace_id": ws, "organization_id": org, "user_id": reader}, "workspace_id = $1 AND user_id = $2", ws, reader)
	fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": ws, "project_id": shared, "subject_type": "organization", "subject_id": org, "role_key": "viewer"})

	fx.Issue(t, "Shared todo", testutil.Cols{"project_id": shared, "status": "todo"})
	fx.Issue(t, "Shared done", testutil.Cols{"project_id": shared, "status": "done"})
	fx.Issue(t, "Shared archived", testutil.Cols{"project_id": shared, "status": "todo", "archived_at": testutil.Raw("now()")})
	fx.Issue(t, "Hidden project issue", testutil.Cols{"project_id": hidden, "status": "todo"})
	fx.Issue(t, "Projectless by reader", testutil.Cols{"creator_type": "member", "creator_id": reader, "status": "todo"})
	fx.Issue(t, "Projectless by other", testutil.Cols{"status": "todo"})

	// The reference count uses the inline predicate that shipped before the
	// materialized rewrite, so a dropped or widened filter fails the comparison.
	inline := issueProjectVisibilityPredicateWithWorkspaceScope("i", "$1", "$2", true)
	expected := fx.Count(t, "SELECT count(*) FROM issue i WHERE i.workspace_id = $1 AND i.archived_at IS NULL AND ("+inline+")", ws, reader)
	active := fx.Count(t, "SELECT count(*) FROM issue i WHERE i.workspace_id = $1 AND i.archived_at IS NULL", ws)
	if expected >= active {
		t.Fatalf("fixture must hide part of the workspace: visible=%d active=%d", expected, active)
	}
	return issueTableVisibilityFixture{fx: fx, ws: ws, reader: reader, expected: expected}
}

func issueTableVisibilityRequest(t *testing.T, userID, workspaceID string, body any) *http.Request {
	t.Helper()
	req := newRequestAs(userID, http.MethodPost, "/api/issues/table/rows", body)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
}

func issueTableVisibilitySpec() issueTableQuerySpec {
	return issueTableQuerySpec{
		Scope: issueTableScope{Kind: "workspace"},
		Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
	}
}

// 2026-09-20 coder(lq): Compare the materialized semi-join with the inline ACL
// predicate on the exact statement shapes rows, groups, and facets build.
func TestIssueTableVisibilityMatchesInlinePredicate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newIssueTableVisibilityFixture(t)
	previous := testHandler.ProjectAuth
	testHandler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	t.Cleanup(func() { testHandler.ProjectAuth = previous })

	compiled, ok := testHandler.compileIssueTableQuery(
		httptest.NewRecorder(),
		issueTableVisibilityRequest(t, fixture.reader, fixture.ws, nil),
		issueTableVisibilitySpec(),
	)
	if !ok {
		t.Fatal("compile failed with the project overlay enabled")
	}
	const materializedRef = "i.id IN (SELECT id FROM issue_auth_visible)"
	if !strings.Contains(compiled.where, materializedRef) {
		t.Fatalf("compiled predicate must reference the materialized set: %q", compiled.where)
	}
	referenceWhere := strings.Replace(compiled.where, materializedRef,
		issueProjectVisibilityPredicateWithWorkspaceScope("i", "$1", "$2", true), 1)

	shapes := []struct{ name, sql string }{
		{"rows_head", `SELECT COALESCE(jsonb_agg(row_snapshot ORDER BY sort_key), '[]'::jsonb) FROM (
			SELECT jsonb_build_object('id', i.id::text, 'status', i.status) AS row_snapshot,
			       COALESCE(i.position::text, '') AS sort_key
			FROM issue i WHERE %s
			ORDER BY i.position ASC NULLS LAST, i.created_at ASC, i.id ASC LIMIT 3
		) page`},
		{"groups", `SELECT COALESCE(jsonb_object_agg(group_value, counts), '{}'::jsonb) FROM (
			SELECT i.status AS group_value, count(*)::bigint AS counts FROM issue i WHERE %s GROUP BY 1
		) grouped`},
		{"facets", `SELECT COALESCE(jsonb_object_agg(bucket, counts), '{}'::jsonb) FROM (
			SELECT CASE WHEN GROUPING(i.status) = 0 THEN i.status ELSE '__total__' END AS bucket,
			       count(*)::bigint AS counts
			FROM issue i WHERE %s GROUP BY GROUPING SETS ((i.status), ())
		) facets`},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			var materialized, reference []byte
			fixture.fx.QueryRow(t, compiled.visibilityWithClause()+fmt.Sprintf(shape.sql, compiled.where), compiled.args...).Scan(&materialized)
			fixture.fx.QueryRow(t, fmt.Sprintf(shape.sql, referenceWhere), compiled.args...).Scan(&reference)
			if !bytes.Equal(materialized, reference) {
				t.Fatalf("materialized visibility changed the result:\nmaterialized=%s\nreference=%s", materialized, reference)
			}
		})
	}
}

// 2026-09-20 coder(lq): Run the three table surfaces through the real handlers so
// the merged WITH list is validated by the server, and cross-check every total
// against the inline predicate.
func TestIssueTableVisibilitySurfacesMatchInlinePredicate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newIssueTableVisibilityFixture(t)
	handler := *testHandler
	handler.ProjectAuth = projectauth.New(newProjectAuthRepository(testPool), true)
	spec := issueTableVisibilitySpec()
	want := int64(fixture.expected)

	rowsRecorder := httptest.NewRecorder()
	handler.ListIssueTableRows(rowsRecorder, issueTableVisibilityRequest(t, fixture.reader, fixture.ws, issueTableRowsRequest{
		Query:     spec,
		Group:     issueTableGroupSpec{Kind: "none"},
		Hierarchy: issueTableHierarchyRequest{Enabled: false},
		Page:      issueTablePageRequest{Limit: 50},
	}))
	if rowsRecorder.Code != http.StatusOK {
		t.Fatalf("rows status = %d: %s", rowsRecorder.Code, rowsRecorder.Body.String())
	}
	var rows issueTableRowsResponse
	if err := json.NewDecoder(rowsRecorder.Body).Decode(&rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	if rows.Total != want || int64(len(rows.Rows)) != want {
		t.Fatalf("rows total=%d rows=%d want %d", rows.Total, len(rows.Rows), want)
	}

	groupsRecorder := httptest.NewRecorder()
	handler.ListIssueTableGroups(groupsRecorder, issueTableVisibilityRequest(t, fixture.reader, fixture.ws, issueTableGroupsRequest{
		Query: spec,
		Group: issueTableGroupSpec{Kind: "status"},
		Page:  issueTablePageRequest{Limit: 50},
	}))
	if groupsRecorder.Code != http.StatusOK {
		t.Fatalf("groups status = %d: %s", groupsRecorder.Code, groupsRecorder.Body.String())
	}
	var groups issueTableGroupsResponse
	if err := json.NewDecoder(groupsRecorder.Body).Decode(&groups); err != nil {
		t.Fatalf("decode groups: %v", err)
	}
	var grouped int64
	for _, group := range groups.Groups {
		grouped += group.Count
	}
	if groups.Total != want || grouped != want {
		t.Fatalf("groups total=%d grouped=%d want %d", groups.Total, grouped, want)
	}

	includeTotal := true
	facetsRecorder := httptest.NewRecorder()
	handler.ListIssueTableFacets(facetsRecorder, issueTableVisibilityRequest(t, fixture.reader, fixture.ws, issueTableFacetsRequest{
		Query:        spec,
		Facets:       []issueTableFacetSpec{{Kind: "status"}},
		IncludeTotal: &includeTotal,
	}))
	if facetsRecorder.Code != http.StatusOK {
		t.Fatalf("facets status = %d: %s", facetsRecorder.Code, facetsRecorder.Body.String())
	}
	var facets issueTableFacetsResponse
	if err := json.NewDecoder(facetsRecorder.Body).Decode(&facets); err != nil {
		t.Fatalf("decode facets: %v", err)
	}
	if facets.Total != want {
		t.Fatalf("facets total=%d want %d", facets.Total, want)
	}
	if len(facets.Facets) != 1 {
		t.Fatalf("facets = %d, want 1", len(facets.Facets))
	}
	var faceted int64
	for _, value := range facets.Facets[0].Values {
		faceted += value.Count
	}
	if faceted != want {
		t.Fatalf("faceted=%d want %d", faceted, want)
	}
}
