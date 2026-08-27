package notify

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type recordingMigrationExecutor struct {
	queries []string
	failAt  int
}

func (e *recordingMigrationExecutor) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	e.queries = append(e.queries, query)
	if e.failAt > 0 && len(e.queries) == e.failAt {
		return nil, errors.New("migration failed")
	}
	return migrationResult(0), nil
}

type migrationResult int64

func (r migrationResult) LastInsertId() (int64, error) { return 0, nil }
func (r migrationResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestEnsureSchemaAppliesModuleFilesInOrder(t *testing.T) {
	exec := &recordingMigrationExecutor{}
	if err := ensureSchema(context.Background(), exec); err != nil {
		t.Fatal(err)
	}
	if len(exec.queries) != 2 {
		t.Fatalf("queries=%d, want 2", len(exec.queries))
	}
	if !strings.Contains(exec.queries[0], "CREATE TABLE IF NOT EXISTS dingtalk_notify_oauth_states") {
		t.Fatal("first migration does not create the OAuth state table")
	}
	if !strings.Contains(exec.queries[1], "CREATE INDEX CONCURRENTLY") {
		t.Fatal("ready index must remain a separate concurrent migration")
	}
}

func TestEnsureSchemaStopsAfterMigrationFailure(t *testing.T) {
	exec := &recordingMigrationExecutor{failAt: 1}
	if err := ensureSchema(context.Background(), exec); err == nil {
		t.Fatal("migration failure should be returned")
	}
	if len(exec.queries) != 1 {
		t.Fatalf("queries=%d, want 1", len(exec.queries))
	}
}
