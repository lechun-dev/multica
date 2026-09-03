package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

func TestProjectPermissionSchemaMissing(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "missing table", err: &pgconn.PgError{Code: "42P01"}, want: true},
		{name: "wrapped missing table", err: errors.Join(errors.New("query failed"), &pgconn.PgError{Code: "42P01"}), want: true},
		{name: "missing column", err: &pgconn.PgError{Code: "42703"}, want: true},
		{name: "different postgres error", err: &pgconn.PgError{Code: "42501"}, want: false},
		{name: "plain error", err: errors.New("query failed"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectPermissionSchemaMissing(tt.err); got != tt.want {
				t.Fatalf("projectPermissionSchemaMissing(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// 2026-09-01 coder(lq): System-role defaults are bootstrap data, not a policy
// floor. Persisted role edits must survive the catalog read performed by the
// update response, including removal of permissions that were defaults.
func TestUpdateSystemRoleDoesNotRestoreRemovedDefaultPermissions(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug)
		VALUES ('Role override test', 'role-override-' || gen_random_uuid()::text)
		RETURNING id::text`).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	repo := &projectAuthRepository{db: tx}
	if err := repo.ensureSystemRoleDefinitions(ctx, workspaceID); err != nil {
		t.Fatalf("seed roles: %v", err)
	}

	updated, err := repo.UpdateRoleDefinition(ctx, workspaceID, string(projectauth.ProjectMember), projectauth.RoleDefinition{
		Name:        "Member",
		Permissions: []projectauth.Permission{projectauth.View},
	})
	if err != nil {
		t.Fatalf("update role: %v", err)
	}
	if len(updated.Permissions) != 1 || updated.Permissions[0] != projectauth.View {
		t.Fatalf("updated permissions = %v, want [%s]", updated.Permissions, projectauth.View)
	}
}
