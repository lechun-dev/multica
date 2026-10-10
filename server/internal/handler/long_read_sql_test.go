package handler

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// 2026-10-10 coder(lq): Reconstruct the released inline search policy without
// duplicating text ranking, comment aggregation, hydration or bind positions.
func inlineSearchReference(query, userRef string, owned bool) string {
	at := strings.Index(query, "issue_matches AS (")
	return strings.Replace("WITH "+query[at:], "i.id IN (SELECT id FROM issue_auth_visible)", issueProjectVisibilityPredicateWithWorkspaceScope("i", "$4", userRef, owned), 1)
}

func inlineUnreadReference(window, owned bool) string {
	filter := "i.workspace_id=$1 AND i.recipient_type='member' AND i.recipient_id=$2 AND i.read=false AND i.archived=false AND " + inboxIssueNotArchivedPredicate("i") + " AND " + inboxIssueProjectVisibilityPredicateWithWorkspaceScope("i", "$1", "$2", owned)
	if window {
		filter += " AND (i.issue_id IS NULL OR " + issueWindowIDPredicate("i.issue_id", "$1", "$3") + ")"
	}
	return "SELECT count(*)::bigint FROM inbox_item i WHERE " + filter
}

func inlineGroupedReference(where, intraGroupOrder, offsetRef, limitRef string) string {
	return fmt.Sprintf(`
WITH ranked AS (
	SELECT
		i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
		i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.position, i.start_date, i.due_date, i.created_at, i.updated_at, i.last_activity_at,
		i.number, i.project_id, i.metadata, i.stage, i.properties, i.revision, i.archived_at, i.duplicate_of_issue_id,
		COUNT(*) OVER (PARTITION BY i.assignee_type, i.assignee_id) AS group_total,
		ROW_NUMBER() OVER (
			PARTITION BY i.assignee_type, i.assignee_id
			ORDER BY %s
		) AS rn
	FROM issue i
	WHERE %s
)
SELECT
	id, workspace_id, title, description, status, priority,
	assignee_type, assignee_id, creator_type, creator_id,
	parent_issue_id, position, start_date, due_date, created_at, updated_at, last_activity_at,
	number, project_id, metadata, stage, properties, revision, archived_at, duplicate_of_issue_id, group_total
FROM ranked
WHERE rn > %s AND rn <= %s + %s
ORDER BY
	CASE assignee_type
		WHEN 'member' THEN 0
		WHEN 'agent' THEN 1
		WHEN 'squad' THEN 2
		ELSE 3
	END,
	assignee_type NULLS LAST,
	assignee_id NULLS LAST,
	rn`, intraGroupOrder, where, offsetRef, offsetRef, limitRef)
}

func inlineWindowSummaryReference(permissions, includeWorkspaceOwned bool) string {
	visibility := ""
	if permissions {
		visibility = fmt.Sprintf(" AND %s", inboxIssueProjectVisibilityPredicateWithWorkspaceScope("i", "policy.workspace_id", "$3", includeWorkspaceOwned))
	}
	return fmt.Sprintf(`WITH policies AS (
	SELECT workspace_id, issue_limit
	FROM unnest($1::uuid[], $2::bigint[]) AS policy(workspace_id, issue_limit)
)
	SELECT policy.workspace_id, filtered.count
	FROM policies policy
	CROSS JOIN LATERAL (
		SELECT COUNT(*)::bigint AS count
		FROM (
			SELECT DISTINCT ON (COALESCE(i.issue_id, i.id)) i.read
			FROM inbox_item i
			JOIN member m ON m.workspace_id = i.workspace_id AND m.user_id = i.recipient_id
			WHERE i.workspace_id = policy.workspace_id
			  AND i.recipient_type = 'member'
			  AND i.recipient_id = $3
			  AND i.archived = false
			  AND %s
			  AND (i.issue_id IS NULL OR %s)%s
			ORDER BY COALESCE(i.issue_id, i.id), i.created_at DESC
		) newest
		WHERE newest.read = false
	) filtered`, inboxIssueNotArchivedPredicate("i"), issueWindowIDPredicate("i.issue_id", "policy.workspace_id", "policy.issue_limit"), visibility)
}

type longSQLPair struct {
	name, before, after string
	args                []any
}

func longSQLFixture(t testing.TB, tasks, grants int) (string, string, string, *testutil.Fixture) {
	ws := dbfx.Workspace(t, "Long SQL", "long-sql")
	reader := dbfx.User(t, "Long SQL reader", "long-sql@example.test")
	fx := testutil.New(testPool, ws, testUserID)
	fx.Member(t, ws, reader, "member")
	fx.Member(t, ws, testUserID, "owner")
	project := fx.Project(t, "Shared project")
	hidden := fx.Project(t, "Hidden project")
	parent := fx.Issue(t, "needle archived parent", testutil.Cols{"project_id": project, "archived_at": testutil.Raw("now()")})
	org := fx.Insert(t, "projectauth_organizations", testutil.Cols{"workspace_id": ws, "provider": "test", "external_id": "long-sql"})
	fx.InsertNoID(t, "projectauth_organization_members", testutil.Cols{"workspace_id": ws, "organization_id": org, "user_id": reader}, "workspace_id=$1", ws)
	grant := fx.Insert(t, "projectauth_access_grants", testutil.Cols{"workspace_id": ws, "project_id": project, "subject_type": "organization", "subject_id": org, "role_key": "viewer"})
	fx.Exec(t, `INSERT INTO projectauth_access_grants (workspace_id,project_id,subject_type,subject_id,role_key)
 SELECT $1,$2,'organization',gen_random_uuid()::text,'viewer' FROM generate_series(1,$3::int)`, ws, project, grants)
	fx.Cleanup(t, "DELETE FROM projectauth_access_grants WHERE workspace_id=$1", ws)
	// 2026-10-10 coder(lq): Mix parent access, hidden tasks, archives, payloads and tied dates.
	fx.Exec(t, `INSERT INTO issue (workspace_id,project_id,parent_issue_id,title,description,status,priority,creator_type,creator_id,assignee_type,assignee_id,number,position,archived_at,start_date,properties)
 SELECT $1,CASE WHEN n%3=0 THEN $2::uuid ELSE $3::uuid END,CASE WHEN n%4=0 THEN $4::uuid END,
 'needle task '||n,repeat('large content ',800),CASE WHEN n%5=0 THEN 'cancelled' ELSE 'todo' END,'none','member',$5,
 CASE WHEN n%2=0 THEN 'member' END,CASE WHEN n%2=0 THEN $6::uuid END,n+1,n%7,CASE WHEN n%11=0 THEN now() END,CASE WHEN n%3=0 THEN current_date+n%7 END,
 CASE WHEN n%4=0 THEN '{}'::jsonb ELSE jsonb_build_object('score',n%5) END
 FROM generate_series(1,$7::int) n`, ws, project, hidden, parent, testUserID, reader, tasks)
	fx.Cleanup(t, "DELETE FROM issue WHERE workspace_id=$1 AND id<>$2", ws, parent)
	fx.Exec(t, `INSERT INTO inbox_item(workspace_id,recipient_type,recipient_id,type,severity,issue_id,title,read,archived,created_at)
 SELECT $1,'member',$2,'status_changed','info',i.id,'Notice',n=3 AND i.number%2=0,false,now()+n*interval '1 second'
 FROM issue i CROSS JOIN generate_series(1,3) n WHERE i.workspace_id=$1`, ws, reader)
	fx.Cleanup(t, "DELETE FROM inbox_item WHERE workspace_id=$1", ws)
	fx.Insert(t, "inbox_item", testutil.Cols{"workspace_id": ws, "recipient_type": "member", "recipient_id": reader, "type": "status_changed", "severity": "info", "title": "System notice", "read": false, "archived": false})
	commentTask := fx.Issue(t, "Separate comment terms", testutil.Cols{"creator_id": reader})
	fx.Comment(t, commentTask, "needle", testutil.Cols{"created_at": testutil.Raw("now()-interval '2 minutes'")})
	fx.Comment(t, commentTask, "task", testutil.Cols{"created_at": testutil.Raw("now()-interval '1 minute'")})
	fx.Comment(t, commentTask, "needle task newest", testutil.Cols{"created_at": testutil.Raw("now()")})
	fx.Issue(t, "needle task custom terminal", testutil.Cols{"creator_id": reader, "status": "custom_done", "properties": testutil.Raw(`'{"score":3}'::jsonb`)})

	fx.Exec(t, "ANALYZE issue")
	fx.Exec(t, "ANALYZE inbox_item")
	fx.Exec(t, "ANALYZE projectauth_access_grants")
	return ws, reader, grant, fx
}

func longSQLPairs(ws, reader string, owned bool, offset int) []longSQLPair {
	q, args := buildSearchQueryWithWorkspaceScope("needle task", []string{"needle", "task"}, 0, false, true, nil, nil, reader, owned)
	args[3] = parseUUID(ws)
	args[len(args)-2] = 5
	args[len(args)-1] = offset
	where := "i.workspace_id=$1 AND i.archived_at IS NULL"
	order := "i.position ASC, i.created_at DESC, i.id DESC"
	pairs := []longSQLPair{
		{"search", inlineSearchReference(q, "$5", owned), q, args},
		{"grouped", inlineGroupedReference(where+" AND "+issueProjectVisibilityPredicateWithWorkspaceScope("i", "$1", "$2", owned), order, "$3", "$4"), groupedIssuePageSQL(where, order, "$3", "$4", "$2", owned), []any{ws, reader, offset, 5}},
		{"unread", inlineUnreadReference(false, owned), unreadInboxCountSQL(false, true, owned), []any{ws, reader}},
		{"window_unread", inlineUnreadReference(true, owned), unreadInboxCountSQL(true, true, owned), []any{ws, reader, int64(40)}},
		{"window_summary", inlineWindowSummaryReference(true, owned), unreadInboxWindowSummarySQL(true, owned), []any{[]pgtype.UUID{parseUUID(ws)}, []int64{40}, parseUUID(reader)}},
	}
	return pairs
}

func additionalLongSQLPairs(ws, reader string, owned bool, offset int) []longSQLPair {
	var pairs []longSQLPair
	window := int64(40)
	for _, spec := range []struct {
		phrase          string
		terms           []string
		number          int
		numeric, closed bool
		window          *int64
		archive         string
	}{
		{"needle", []string{"needle"}, 0, false, true, nil, "active"},
		{"2", []string{"2"}, 2, true, true, nil, "active"},
		{"needle task", []string{"needle", "task"}, 0, false, false, &window, "active"},
		{"needle", []string{"needle"}, 0, false, true, nil, "archived"},
	} {
		q, args := buildSearchQueryWithWorkspaceScope(spec.phrase, spec.terms, spec.number, spec.numeric, spec.closed, []string{"done", "cancelled", "custom_done"}, spec.window, reader, owned, spec.archive)
		args[3] = parseUUID(ws)
		args[len(args)-2] = 5
		args[len(args)-1] = offset
		pairs = append(pairs, longSQLPair{"search-options", inlineSearchReference(q, "$5", owned), q, args})
	}
	for _, order := range []string{"i.start_date ASC NULLS LAST, i.created_at DESC, i.id DESC", "(i.properties->>'score')::numeric DESC NULLS LAST, i.created_at DESC, i.id DESC"} {
		where := "i.workspace_id=$1 AND i.archived_at IS NULL AND i.status='todo'"
		pairs = append(pairs, longSQLPair{"grouped-options", inlineGroupedReference(where+" AND "+issueProjectVisibilityPredicateWithWorkspaceScope("i", "$1", "$2", owned), order, "$3", "$4"), groupedIssuePageSQL(where, order, "$3", "$4", "$2", owned), []any{ws, reader, offset, 5}})
		pairs = append(pairs, longSQLPair{"grouped-disabled", inlineGroupedReference(where, order, "$2", "$3"), groupedIssuePageSQL(where, order, "$2", "$3", "", owned), []any{ws, offset, 5}})
	}
	pairs = append(pairs, longSQLPair{"window-summary-disabled", inlineWindowSummaryReference(false, owned), unreadInboxWindowSummarySQL(false, owned), []any{[]pgtype.UUID{parseUUID(ws)}, []int64{40}, parseUUID(reader)}})
	return pairs
}

func longSQLRows(t testing.TB, query string, args []any) [][]any {
	t.Helper()
	rows, err := testPool.Query(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result [][]any
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLongReadSQLParity(t *testing.T) {
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	ws, reader, grant, fx := longSQLFixture(t, 80, 30)
	other := dbfx.Workspace(t, "Other summary policy", "other-summary-policy")
	empty := dbfx.Workspace(t, "Empty summary policy", "empty-summary-policy")
	otherFixture := testutil.New(testPool, other, testUserID)
	otherFixture.Member(t, other, reader, "member")
	otherIssue := otherFixture.Issue(t, "Independent window", testutil.Cols{"creator_id": reader})
	otherFixture.Insert(t, "inbox_item", testutil.Cols{"workspace_id": other, "recipient_type": "member", "recipient_id": reader, "type": "status_changed", "severity": "info", "issue_id": otherIssue, "title": "Other policy notice", "read": false, "archived": false})
	check := func() {
		for _, user := range []string{reader, testUserID} {
			for _, owned := range []bool{false, true} {
				for _, offset := range []int{0, 2, 100} {
					pairs := append(longSQLPairs(ws, user, owned, offset), additionalLongSQLPairs(ws, user, owned, offset)...)
					// 2026-10-10 coder(lq): LATERAL policy scopes must remain independent,
					// including a workspace with no notices and a different window size.
					pairs = append(pairs, longSQLPair{"multiple-window-policies", inlineWindowSummaryReference(true, owned), unreadInboxWindowSummarySQL(true, owned), []any{[]pgtype.UUID{parseUUID(ws), parseUUID(other), parseUUID(empty)}, []int64{40, 1, 2}, parseUUID(user)}})
					for _, pair := range pairs {
						before := longSQLRows(t, pair.before, pair.args)
						after := longSQLRows(t, pair.after, pair.args)
						if !reflect.DeepEqual(before, after) {
							t.Fatalf("%s user=%s owned=%v offset=%d mismatch\nbefore=%v\nafter=%v", pair.name, user, owned, offset, before, after)
						}
					}
				}
			}
		}
	}
	check()
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "true")
	check()
	t.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	fx.InsertNoID(t, "projectauth_grant_constraints", testutil.Cols{"workspace_id": ws, "grant_id": grant, "expires_at": testutil.Raw("now()-interval '1 minute'")}, "grant_id=$1", grant)
	check()
	fx.Exec(t, "DELETE FROM projectauth_grant_constraints WHERE grant_id=$1", grant)
	fx.Exec(t, "DELETE FROM projectauth_access_grants WHERE id=$1", grant)
	check()
	fx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", ws, reader)
	check()
}

func BenchmarkLongReadSQL(b *testing.B) {
	b.Setenv("PROJECT_OWNER_BYPASS_ENABLED", "false")
	ws, reader, _, _ := longSQLFixture(b, 700, 1300)
	pairs := longSQLPairs(ws, reader, true, 0)
	// 2026-10-10 coder(lq): Dynamic filter combinations also incur planning.
	// Measure that separately from warmed default prepared-statement execution.
	for _, pair := range pairs[:2] {
		pair.name += "_planning"
		pair.args = append([]any{pgx.QueryExecModeDescribeExec}, pair.args...)
		pairs = append(pairs, pair)
	}
	for _, pair := range pairs {
		for _, version := range []struct{ name, query string }{{"before", pair.before}, {"after", pair.after}} {
			b.Run(fmt.Sprintf("%s/%s", pair.name, version.name), func(b *testing.B) {
				for range 10 {
					longSQLRows(b, version.query, pair.args)
				}
				for b.Loop() {
					longSQLRows(b, version.query, pair.args)
				}
			})
		}
	}
}
