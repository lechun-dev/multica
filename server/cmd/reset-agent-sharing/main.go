// reset-agent-sharing is an explicit, offline permission maintenance tool.
// It never runs during server startup, migrations, or development setup.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

type identity struct {
	Database string `json:"database"`
	Address  string `json:"server_address"`
	Port     int    `json:"server_port"`
}
type agentGrant struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Owner      *string `json:"owner_id"`
	Mode       string  `json:"permission_mode"`
	Visibility string  `json:"visibility"`
	Updated    string  `json:"updated_at"`
}
type targetGrant struct {
	ID      string  `json:"id"`
	Agent   string  `json:"agent_id"`
	Type    string  `json:"target_type"`
	Target  string  `json:"target_id"`
	Creator *string `json:"created_by"`
	Created string  `json:"created_at"`
}
type snapshot struct {
	Version  int           `json:"version"`
	Database identity      `json:"database"`
	Agents   []agentGrant  `json:"agents"`
	Targets  []targetGrant `json:"targets"`
	Excluded int           `json:"excluded_system_agents"`
}
type receipt struct {
	Version    int      `json:"version"`
	PreviewSHA string   `json:"preview_sha256"`
	Before     snapshot `json:"before"`
	After      snapshot `json:"after"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reset-agent-sharing", flag.ContinueOnError)
	mode := fs.String("mode", "preview", "preview, apply, or rollback")
	expected := fs.String("expected-database", "", "required exact database name")
	file := fs.String("snapshot", "", "preview snapshot path (created exclusively, mode 0600)")
	receiptFile := fs.String("receipt", "", "apply receipt path (required for apply and rollback)")
	digest := fs.String("sha256", "", "reviewed preview digest (required for apply)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *expected == "" {
		return errors.New("--expected-database is required; no default database is allowed")
	}
	if *mode != "preview" && *mode != "apply" && *mode != "rollback" {
		return errors.New("unknown mode")
	}
	if *mode != "rollback" && *file == "" {
		return errors.New("--snapshot is required")
	}
	if *mode != "preview" && *receiptFile == "" {
		return errors.New("--receipt is required")
	}
	if *mode == "apply" && len(*digest) != 64 {
		return errors.New("--sha256 must be the reviewed preview digest")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("explicit DATABASE_URL is required; .env files are never loaded")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return errors.New("could not connect to the explicitly supplied database")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET TIME ZONE 'UTC'`); err != nil {
		return err
	}
	isolation := pgx.ReadCommitted
	if *mode == "preview" {
		isolation = pgx.RepeatableRead
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='30s'`); err != nil {
		return err
	}
	var db identity
	if err := tx.QueryRow(ctx, `SELECT current_database(), COALESCE(inet_server_addr()::text,'local-socket'), COALESCE(inet_server_port(),0)`).Scan(&db.Database, &db.Address, &db.Port); err != nil {
		return err
	}
	if db.Database != *expected {
		return fmt.Errorf("database mismatch: connected to %q, expected %q; no changes made", db.Database, *expected)
	}
	if *mode != "preview" {
		// Prevent new agents/targets and concurrent permission writers during the
		// compare-and-write transaction. A busy environment fails rather than waits.
		if _, err := tx.Exec(ctx, `LOCK TABLE agent, agent_invocation_target IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("permission tables busy; no changes made: %w", err)
		}
	}
	current, err := capture(ctx, tx, db)
	if err != nil {
		return err
	}
	if *mode == "preview" {
		body, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			return err
		}
		body = append(body, '\n')
		if err := writeExclusive(*file, body); err != nil {
			return err
		}
		affected, ownerless := counts(current)
		fmt.Printf("Preview only: database=%s server=%s:%d user_agents=%d affected=%d ownerless=%d excluded_system_agents=%d\nSnapshot: %s\nSHA256: %s\n", db.Database, db.Address, db.Port, len(current.Agents), affected, ownerless, current.Excluded, *file, checksum(body))
		return tx.Commit(ctx)
	}
	if *mode == "apply" {
		body, err := readFile(*file)
		if err != nil {
			return err
		}
		if checksum(body) != *digest {
			return errors.New("preview digest mismatch; no changes made")
		}
		var before snapshot
		if err := json.Unmarshal(body, &before); err != nil {
			return err
		}
		if before.Version != 1 || !equal(before, current) {
			return errors.New("database identity or agent state changed since preview; generate a new preview; no changes made")
		}
		for _, a := range before.Agents {
			if !affectedAgent(a, before.Targets) {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE agent SET permission_mode='private', visibility='private', updated_at=clock_timestamp() WHERE id=$1`, a.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM agent_invocation_target WHERE agent_id=$1`, a.ID); err != nil {
				return err
			}
		}
		after, err := capture(ctx, tx, db)
		if err != nil {
			return err
		}
		record := receipt{Version: 1, PreviewSHA: *digest, Before: before, After: after}
		body, err = json.MarshalIndent(record, "", "  ")
		if err != nil {
			return err
		}
		// Persist recovery information BEFORE commit. If commit fails, rollback
		// compares the receipt to live state and refuses an unapplied operation.
		if err := writeExclusive(*receiptFile, append(body, '\n')); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit uncertain; preserve receipt and inspect database before retry: %w", err)
		}
		changed, _ := counts(before)
		fmt.Printf("Applied permission reset to %d agents in %s. Receipt: %s\n", changed, db.Database, *receiptFile)
		return nil
	}
	body, err := readFile(*receiptFile)
	if err != nil {
		return err
	}
	var record receipt
	if err := json.Unmarshal(body, &record); err != nil {
		return err
	}
	if record.Version != 1 || !equal(record.After, current) {
		return errors.New("database state differs from reset receipt; rollback refused to preserve subsequent edits")
	}
	for _, a := range record.Before.Agents {
		if !affectedAgent(a, record.Before.Targets) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE agent SET permission_mode=$2, visibility=$3, updated_at=clock_timestamp() WHERE id=$1`, a.ID, a.Mode, a.Visibility); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agent_invocation_target WHERE agent_id=$1`, a.ID); err != nil {
			return err
		}
	}
	for _, t := range record.Before.Targets {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_invocation_target(id,agent_id,target_type,target_id,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6::timestamptz)`, t.ID, t.Agent, t.Type, t.Target, t.Creator, t.Created); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Printf("Restored original sharing in %s from %s.\n", db.Database, *receiptFile)
	return nil
}

func capture(ctx context.Context, tx pgx.Tx, db identity) (snapshot, error) {
	out := snapshot{Version: 1, Database: db, Agents: []agentGrant{}, Targets: []targetGrant{}}
	rows, err := tx.Query(ctx, `SELECT id::text,name,owner_id::text,permission_mode,visibility,updated_at::text FROM agent WHERE kind='user' ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var a agentGrant
		if err := rows.Scan(&a.ID, &a.Name, &a.Owner, &a.Mode, &a.Visibility, &a.Updated); err != nil {
			rows.Close()
			return out, err
		}
		out.Agents = append(out.Agents, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT t.id::text,t.agent_id::text,t.target_type,t.target_id::text,t.created_by::text,t.created_at::text FROM agent_invocation_target t JOIN agent a ON a.id=t.agent_id WHERE a.kind='user' ORDER BY t.agent_id,t.id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var t targetGrant
		if err := rows.Scan(&t.ID, &t.Agent, &t.Type, &t.Target, &t.Creator, &t.Created); err != nil {
			rows.Close()
			return out, err
		}
		out.Targets = append(out.Targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM agent WHERE kind<>'user'`).Scan(&out.Excluded)
	return out, err
}
func affectedAgent(a agentGrant, targets []targetGrant) bool {
	if a.Mode != "private" || a.Visibility != "private" {
		return true
	}
	for _, t := range targets {
		if t.Agent == a.ID {
			return true
		}
	}
	return false
}
func counts(s snapshot) (affected, ownerless int) {
	for _, a := range s.Agents {
		if affectedAgent(a, s.Targets) {
			affected++
		}
		if a.Owner == nil {
			ownerless++
		}
	}
	return
}
func equal(a, b snapshot) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func checksum(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func readFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return nil, errors.New("snapshot/receipt must be a regular file under 32 MiB")
	}
	return os.ReadFile(path)
}
func writeExclusive(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	// Sync the containing directory so the recovery filename survives a crash.
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0600 {
		return errors.New("recovery file must have mode 0600")
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
