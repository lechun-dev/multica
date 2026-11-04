package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestResetSharingRequiresExplicitTarget(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("missing database accepted")
	}
}

// This integration test cannot target an existing developer or remote database.
// It uses a dedicated schema in the independently provisioned test-only cluster.
func TestResetSharingPreviewApplyRollback(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Database != "multica_optimization_test" || config.Host != "127.0.0.1" || config.Port != 55483 {
		t.Skip("requires explicit isolated permission test database")
	}
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET TIME ZONE 'UTC'`); err != nil {
		t.Fatal(err)
	}
	schema := "sharing_reset_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	testURL, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := testURL.Query()
	params.Set("search_path", schema)
	testURL.RawQuery = params.Encode()
	t.Setenv("DATABASE_URL", testURL.String())
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `CREATE TABLE agent(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),name text,owner_id uuid,permission_mode text,visibility text,updated_at timestamptz DEFAULT now(),kind text DEFAULT 'user');
 CREATE TABLE agent_invocation_target(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),agent_id uuid,target_type text,target_id uuid,created_by uuid,created_at timestamptz DEFAULT now());
 INSERT INTO agent(name,owner_id,permission_mode,visibility) VALUES('shared',gen_random_uuid(),'public_to','workspace'),('private',gen_random_uuid(),'private','private'),('ownerless',NULL,'public_to','workspace');
 INSERT INTO agent(name,permission_mode,visibility,kind) VALUES('system','public_to','workspace','system');
 INSERT INTO agent_invocation_target(agent_id,target_type,target_id) SELECT id,'workspace',gen_random_uuid() FROM agent WHERE permission_mode='public_to'`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	snap := filepath.Join(dir, "preview.json")
	rec := filepath.Join(dir, "receipt.json")
	base := []string{"--expected-database", "multica_optimization_test", "--snapshot", snap}
	if err := run(ctx, base); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(snap)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot is not private")
	}
	var before snapshot
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if n, orphan := counts(before); n != 2 || orphan != 1 || before.Excluded != 1 {
		t.Fatalf("preview counts: %d %d %d", n, orphan, before.Excluded)
	}
	apply := append(append([]string{}, base...), "--mode", "apply", "--receipt", rec, "--sha256", checksum(raw))
	rollback := []string{"--mode", "rollback", "--expected-database", "multica_optimization_test", "--receipt", rec}
	// Identity, digest and stale-preview checks must leave all grants intact.
	if err := run(ctx, []string{"--expected-database", "wrong", "--snapshot", filepath.Join(dir, "wrong.json")}); err == nil {
		t.Fatal("wrong database accepted")
	}
	wrong := append(append([]string{}, base...), "--mode", "apply", "--receipt", rec, "--sha256", strings.Repeat("0", 64))
	if err := run(ctx, wrong); err == nil {
		t.Fatal("wrong digest accepted")
	}
	if _, err := conn.Exec(ctx, `UPDATE agent SET updated_at=clock_timestamp() WHERE name='shared'`); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, apply); err == nil {
		t.Fatal("stale preview accepted")
	}
	// Repreview to a fresh path; existing recovery artifacts are never overwritten.
	if err := run(ctx, base); err == nil {
		t.Fatal("existing snapshot overwritten")
	}
	snap = filepath.Join(dir, "preview-current.json")
	base = []string{"--expected-database", "multica_optimization_test", "--snapshot", snap}
	if err := run(ctx, base); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(snap)
	apply = append(append([]string{}, base...), "--mode", "apply", "--receipt", rec, "--sha256", checksum(raw))
	if err := run(ctx, apply); err != nil {
		t.Fatal(err)
	}
	var remaining, systems int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM agent WHERE kind='user' AND (permission_mode<>'private' OR visibility<>'private')`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("user sharing remains")
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM agent_invocation_target t JOIN agent a ON a.id=t.agent_id WHERE a.kind='system'`).Scan(&systems); err != nil {
		t.Fatal(err)
	}
	if systems != 1 {
		t.Fatal("system sharing altered")
	}
	if err := run(ctx, rollback); err != nil {
		t.Fatal(err)
	}
	var restored int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM agent_invocation_target t JOIN agent a ON a.id=t.agent_id WHERE a.kind='user'`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != len(before.Targets) {
		t.Fatalf("restored %d targets, expected %d", restored, len(before.Targets))
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterRollback, err := capture(ctx, tx, before.Database)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Reset and rollback change updated_at, but every grant attribute and
	// permission mode must round-trip exactly, including creator and timestamps.
	for i := range afterRollback.Agents {
		afterRollback.Agents[i].Updated = before.Agents[i].Updated
	}
	if !equal(before, afterRollback) {
		t.Fatal("rollback did not restore the exact permission snapshot")
	}
	if err := run(ctx, rollback); err == nil {
		t.Fatal("old receipt applied again")
	}
	// A later edit prevents rollback of a subsequent reset.
	snap2 := filepath.Join(dir, "preview-second.json")
	rec2 := filepath.Join(dir, "receipt-second.json")
	base = []string{"--expected-database", "multica_optimization_test", "--snapshot", snap2}
	if err := run(ctx, base); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(snap2)
	apply = append(append([]string{}, base...), "--mode", "apply", "--receipt", rec2, "--sha256", checksum(raw))
	if err := run(ctx, apply); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE agent SET name='edited',updated_at=clock_timestamp() WHERE name='shared'`); err != nil {
		t.Fatal(err)
	}
	rollback = []string{"--mode", "rollback", "--expected-database", "multica_optimization_test", "--receipt", rec2}
	if err := run(ctx, rollback); err == nil {
		t.Fatal("rollback overwrote a later edit")
	}
}
