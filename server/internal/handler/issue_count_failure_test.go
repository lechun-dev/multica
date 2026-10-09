package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type issueCountFailureDB struct {
	dbExecutor
	failures      int
	failPageCount bool
}

// 2026-10-09 coder(lq): Counts on nonempty pages share the row statement;
// inject at that boundary while preserving the empty-page fallback coverage.
func (db *issueCountFailureDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if db.failPageCount && strings.Contains(sql, "COUNT(*) OVER()") {
		db.failures++
		return nil, errors.New("injected COUNT failure: private SQL details")
	}
	return db.dbExecutor.Query(ctx, sql, args...)
}

func (db *issueCountFailureDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "SELECT COUNT(*) FROM issue i WHERE ") {
		db.failures++
		return issueCountFailureRow{}
	}
	return db.dbExecutor.QueryRow(ctx, sql, args...)
}

type issueCountFailureRow struct{}

func (issueCountFailureRow) Scan(...any) error {
	return errors.New("injected COUNT failure: private SQL details")
}

func TestListIssuesCountFailureDoesNotReturnPartialSuccess(t *testing.T) {
	projectID := dbfx.Project(t, "Count failure")
	for i := 0; i < 3; i++ {
		dbfx.Issue(t, fmt.Sprintf("Counted issue %d", i), testutil.Cols{"project_id": projectID})
	}
	for _, offset := range []int{0, 2, 100} {
		t.Run(fmt.Sprintf("offset_%d", offset), func(t *testing.T) {
			path := fmt.Sprintf("/api/issues?workspace_id=%s&project_id=%s&limit=2&offset=%d", testWorkspaceID, projectID, offset)
			// Prove that rows and the count are normally available for this query.
			var healthy struct {
				Issues []IssueResponse `json:"issues"`
				Total  int             `json:"total"`
			}
			testutil.Call(t, testHandler.ListIssues, newRequest("GET", path, nil)).Want(http.StatusOK).JSON(&healthy)
			if healthy.Total != 3 || len(healthy.Issues) != max(0, min(2, 3-offset)) {
				t.Fatalf("unexpected healthy page: %+v", healthy)
			}

			h := *testHandler
			failingDB := &issueCountFailureDB{dbExecutor: h.DB, failPageCount: offset < 3}
			h.DB = failingDB
			body := testutil.Call(t, h.ListIssues, newRequest("GET", path, nil)).Want(http.StatusInternalServerError).Map()
			if failingDB.failures != 1 {
				t.Fatalf("injected %d count failures, want 1", failingDB.failures)
			}
			wantError := "failed to count issues"
			if offset < 3 {
				wantError = "failed to list issues"
			}
			if len(body) != 1 || body["error"] != wantError {
				t.Fatalf("failure must expose only a public error, not issues, total, or SQL: %v", body)
			}
		})
	}
}
