package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	testPool        *pgxpool.Pool
	testWorkspaceID string
	testUserID      string
)

const (
	fixtureEmail = "testutil-fixture@multica.ai"
	fixtureSlug  = "testutil-fixtures"
)

// TestMain builds the workspace the fixture builders hang off, in the same
// shape internal/handler's TestMain builds it. Without a database the suite
// exits green rather than red: the same contract every other DB-backed package
// here follows, so a laptop without Postgres still runs the rest of the tree.
func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping DB fixture tests: could not connect: %v\n", err)
		os.Exit(m.Run())
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping DB fixture tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(m.Run())
	}

	if err := seedFixtureWorkspace(ctx, pool); err != nil {
		fmt.Printf("Skipping DB fixture tests: seed failed: %v\n", err)
		pool.Close()
		os.Exit(m.Run())
	}
	testPool = pool

	code := m.Run()
	_, _ = pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, fixtureSlug)
	_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, fixtureEmail)
	pool.Close()
	os.Exit(code)
}

func seedFixtureWorkspace(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, fixtureSlug); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, fixtureEmail); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Testutil Fixture User", fixtureEmail).Scan(&testUserID); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $2, $3) RETURNING id`,
		"Testutil Fixtures", fixtureSlug, "TUF").Scan(&testWorkspaceID); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		testWorkspaceID, testUserID)
	return err
}

func newFixture(t *testing.T) *Fixture {
	t.Helper()
	if testPool == nil {
		t.Skip("database not available")
	}
	return New(testPool, testWorkspaceID, testUserID)
}

// TestBuildersInsertRowsTheSchemaAccepts is the test that makes the shared
// defaults safe to migrate onto: every builder writes a real row against the
// real schema, so a column that gets added NOT NULL without a default breaks
// here — once — instead of in each of the suites that adopted the builder.
func TestBuildersInsertRowsTheSchemaAccepts(t *testing.T) {
	f := newFixture(t)

	runtimeID := f.Runtime(t, "fixture runtime")
	agentID := f.Agent(t, "fixture agent", runtimeID)
	issueID := f.Issue(t, "fixture issue")
	commentID := f.Comment(t, issueID, "fixture comment")
	taskID := f.Task(t, agentID, Cols{"runtime_id": runtimeID, "issue_id": issueID})
	squadID := f.Squad(t, "fixture squad", agentID)
	f.SquadMember(t, squadID, "agent", agentID)
	projectID := f.Project(t, "fixture project")
	sessionID := f.ChatSession(t, agentID)

	for _, tt := range []struct {
		table string
		id    string
	}{
		{"agent_runtime", runtimeID},
		{"agent", agentID},
		{"issue", issueID},
		{"comment", commentID},
		{"agent_task_queue", taskID},
		{"squad", squadID},
		{"project", projectID},
		{"chat_session", sessionID},
	} {
		if n := f.Count(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id = $1`, tt.table), tt.id); n != 1 {
			t.Fatalf("%s: expected the fixture row to exist, found %d", tt.table, n)
		}
	}
}

// TestInsertedRowsAreGoneAfterTheirTest pins the half of the contract the
// builders exist for. Rows that outlive their test are what forced suites to
// hand-write a DELETE beside every INSERT, and a builder that quietly stopped
// registering cleanup would leak into the next test's counts instead of
// failing.
func TestInsertedRowsAreGoneAfterTheirTest(t *testing.T) {
	f := newFixture(t)

	var issueID string
	t.Run("creates", func(t *testing.T) {
		issueID = f.Issue(t, "disappearing issue")
		if n := f.Count(t, `SELECT count(*) FROM issue WHERE id = $1`, issueID); n != 1 {
			t.Fatalf("expected the issue to exist inside its test, found %d", n)
		}
	})

	if n := f.Count(t, `SELECT count(*) FROM issue WHERE id = $1`, issueID); n != 0 {
		t.Fatalf("issue survived the test that created it, found %d", n)
	}
}

// TestOverridesReplaceDefaults keeps the builders honest about which value
// wins: a test that names a column is describing the thing it asserts on, so a
// default that quietly survived would make the assertion test the fixture
// rather than the code.
func TestOverridesReplaceDefaults(t *testing.T) {
	f := newFixture(t)

	issueID := f.Issue(t, "overridden", Cols{"status": "in_progress", "priority": "urgent"})
	var status, priority string
	f.QueryRow(t, `SELECT status, priority FROM issue WHERE id = $1`, issueID).Scan(&status, &priority)
	if status != "in_progress" || priority != "urgent" {
		t.Fatalf("status/priority = %q/%q, want in_progress/urgent", status, priority)
	}
}

// TestRawValuesAreInlinedAsSQL covers the escape hatch the timing fixtures
// need: `dispatched_at` in the past cannot be written as a bound Go value
// without the test pinning a clock the query does not read.
func TestRawValuesAreInlinedAsSQL(t *testing.T) {
	f := newFixture(t)

	runtimeID := f.Runtime(t, "raw runtime")
	agentID := f.Agent(t, "raw agent", runtimeID)
	taskID := f.Task(t, agentID, Cols{
		"runtime_id":    runtimeID,
		"status":        "dispatched",
		"dispatched_at": Raw("now() - interval '2 hours'"),
	})

	var ageSeconds float64
	f.QueryRow(t, `SELECT EXTRACT(EPOCH FROM (now() - dispatched_at)) FROM agent_task_queue WHERE id = $1`, taskID).
		Scan(&ageSeconds)
	if ageSeconds < 7100 {
		t.Fatalf("dispatched_at is %.0fs old, want ~7200s — the Raw expression was bound as a value", ageSeconds)
	}
}

// TestIssueNumbersDoNotCollide covers the one default that cannot be a
// constant: issue.number is unique per workspace, so two issues built by one
// test have to negotiate it.
func TestIssueNumbersDoNotCollide(t *testing.T) {
	f := newFixture(t)

	first := f.Issue(t, "first")
	second := f.Issue(t, "second")

	var a, b int
	f.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, first).Scan(&a)
	f.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, second).Scan(&b)
	if a == b {
		t.Fatalf("both issues took number %d", a)
	}
}

func issueCounterFixture(t *testing.T) *Fixture {
	t.Helper()
	f := newFixture(t)
	ws := f.Workspace(t, "Issue counter", "fixture-counter-"+uuid.NewString())
	return New(f.Pool, ws, f.UserID)
}

// createApplicationIssue uses the same counter and insert queries as ordinary
// creates and handover checklists, without going through the fixture builder.
func createApplicationIssue(t *testing.T, f *Fixture) int32 {
	t.Helper()
	ctx := context.Background()
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var workspaceID, creatorID pgtype.UUID
	if err := workspaceID.Scan(f.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := creatorID.Scan(f.UserID); err != nil {
		t.Fatal(err)
	}
	q := db.New(tx)
	number, err := q.IncrementIssueCounter(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := q.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: workspaceID, Title: "Application issue", Status: "todo",
		Priority: "none", CreatorType: "member", CreatorID: creatorID, Number: number,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	f.deleteOnCleanup(t, "issue", uuid.UUID(issue.ID.Bytes).String())
	return issue.Number
}

func TestIssueFixturesShareApplicationCounter(t *testing.T) {
	f := issueCounterFixture(t)
	f.Issue(t, "Fixture before checklist")
	if number := createApplicationIssue(t, f); number != 2 {
		t.Fatalf("application issue number = %d, want 2", number)
	}
	last := f.Issue(t, "Fixture after checklist")
	var number, counter int32
	f.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, last).Scan(&number)
	f.QueryRow(t, `SELECT issue_counter FROM workspace WHERE id = $1`, f.WorkspaceID).Scan(&counter)
	if number != 3 || counter != 3 {
		t.Fatalf("fixture number/counter = %d/%d, want 3/3", number, counter)
	}
}

func TestIssueFixtureExplicitNumbersNeverRewindCounter(t *testing.T) {
	f := issueCounterFixture(t)
	f.Issue(t, "Higher explicit number", Cols{"number": 100})
	f.Issue(t, "Lower explicit number", Cols{"number": Raw("50")})
	if number := createApplicationIssue(t, f); number != 101 {
		t.Fatalf("application issue number = %d, want 101", number)
	}
}

func TestIssueFixturesSerializeWithApplicationCreates(t *testing.T) {
	f := issueCounterFixture(t)
	const workers = 8
	numbers := make(chan int32, workers)
	t.Run("concurrent creates", func(t *testing.T) {
		for i := range workers {
			t.Run(fmt.Sprintf("create-%d", i), func(t *testing.T) {
				t.Parallel()
				if i%2 == 0 {
					numbers <- createApplicationIssue(t, f)
					return
				}
				id := f.Issue(t, "Concurrent fixture")
				var number int32
				f.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, id).Scan(&number)
				numbers <- number
			})
		}
	})
	close(numbers)
	seen := make(map[int32]bool)
	for number := range numbers {
		if seen[number] {
			t.Fatalf("duplicate issue number %d", number)
		}
		seen[number] = true
	}
	var counter int32
	f.QueryRow(t, `SELECT issue_counter FROM workspace WHERE id = $1`, f.WorkspaceID).Scan(&counter)
	if len(seen) != workers || counter != workers {
		t.Fatalf("unique numbers/counter = %d/%d, want %d/%d", len(seen), counter, workers, workers)
	}
}

// TestUserTableIsQuoted guards the identifier quoting: `user` is reserved, and
// an unquoted INSERT INTO user is a syntax error rather than a wrong row.
func TestUserTableIsQuoted(t *testing.T) {
	f := newFixture(t)

	userID := f.User(t, "Quoted Name", "testutil-quoted@multica.ai")
	var name string
	f.QueryRow(t, `SELECT name FROM "user" WHERE id = $1`, userID).Scan(&name)
	if name != "Quoted Name" {
		t.Fatalf("name = %q, want Quoted Name", name)
	}
}
