package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTaskMemberAgentUseMigrationIsScopedIdempotentAndReversible(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// 2026-10-09 coder(lq): Session-local tables isolate the upgrade/rollback
	// assertions from the real task-role catalog in the integration database.
	_, err = tx.Exec(ctx, `
		CREATE TEMP TABLE projectauth_task_roles (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), role_key text, is_system boolean);
		CREATE TEMP TABLE projectauth_task_role_permissions (role_id uuid, permission text, PRIMARY KEY (role_id, permission));
		INSERT INTO projectauth_task_roles (role_key, is_system) VALUES ('member', true), ('viewer', true), ('custom', false);
		INSERT INTO projectauth_task_role_permissions SELECT id, 'project.agent.use' FROM projectauth_task_roles WHERE role_key='custom';
	`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		applyMigrationFile(t, ctx, tx, "601_task_member_agent_use.up.sql")
	}
	assertRoles := func(want []string) {
		t.Helper()
		var got []string
		if err := tx.QueryRow(ctx, `SELECT array_agg(role.role_key ORDER BY role.role_key)
			FROM projectauth_task_role_permissions permission JOIN projectauth_task_roles role ON role.id=permission.role_id
			WHERE permission.permission='project.agent.use'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("execution roles = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("execution roles = %v, want %v", got, want)
			}
		}
	}
	assertRoles([]string{"custom", "member"})
	applyMigrationFile(t, ctx, tx, "601_task_member_agent_use.down.sql")
	assertRoles([]string{"custom"})
}
