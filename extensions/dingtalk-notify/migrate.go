package notify

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

const schemaMigrationLockID int64 = 0x4d554c5444544e

//go:embed migrations/001_dingtalk_notify.sql
var schemaMigrationSQL string

//go:embed migrations/002_dingtalk_notify_outbox_ready_idx.sql
var readyIndexMigrationSQL string

type migrationExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// EnsureSchema applies the isolated module schema on a dedicated PostgreSQL
// connection. The advisory lock serializes startup across replicas, while the
// concurrent index remains a separate statement outside a transaction.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("DingTalk schema database is required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire DingTalk schema connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", schemaMigrationLockID); err != nil {
		return fmt.Errorf("lock DingTalk schema migration: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", schemaMigrationLockID)
	}()

	return ensureSchema(ctx, conn)
}

func ensureSchema(ctx context.Context, exec migrationExecutor) error {
	if exec == nil {
		return errors.New("DingTalk schema executor is required")
	}
	for _, migration := range []struct {
		name string
		sql  string
	}{
		{name: "001_dingtalk_notify.sql", sql: schemaMigrationSQL},
		{name: "002_dingtalk_notify_outbox_ready_idx.sql", sql: readyIndexMigrationSQL},
	} {
		if _, err := exec.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply DingTalk migration %s: %w", migration.name, err)
		}
	}
	return nil
}
