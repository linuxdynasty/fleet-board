package statedb_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Migration 9 on the file itself: one row per command_id, a seq issued
// once per machine and epoch, a closed outbox row only as a DONE
// Operation, a send recorded whole, the 1 KiB message cap, and a result
// that never changes once stored.
func TestTheCommandsSchemaBacksTheOutboxRules(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db := rawDB(t, dir)
	ctx := t.Context()
	ins := `INSERT INTO commands(command_id, machine_id, kind, command, create_time, not_after_time, update_time, hub_seq,
		sent_epoch, sent_seq, sent_time) VALUES (?, 'm1', 'interrupt', x'00', 1, 2, 1, 1, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, ins, "c1", 3, 1, 5); err != nil {
		t.Fatal(err)
	}
	refused := []struct {
		name string
		sql  string
		args []any
	}{
		{"a second command_id", ins, []any{"c1", nil, nil, nil}},
		{"a seq twice in one epoch", ins, []any{"c2", 3, 1, 5}},
		{"a send without its seq", ins, []any{"c3", 3, nil, 5}},
		{"a deadline before the command", `INSERT INTO commands(command_id, machine_id, kind, command, create_time,
			not_after_time, update_time, hub_seq) VALUES ('c4', 'm1', 'k', x'00', 5, 5, 5, 1)`, nil},
		{"a closed row that is not DONE", `UPDATE commands SET state = 'done' WHERE command_id = 'c1'`, nil},
		{"a DONE row still pending", `UPDATE commands SET op_state = 'DONE' WHERE command_id = 'c1'`, nil},
		{"a message over 1 KiB", `UPDATE commands SET message = printf('%.1025c', 'x') WHERE command_id = 'c1'`, nil},
	}
	for _, r := range refused {
		if _, err := db.ExecContext(ctx, r.sql, r.args...); err == nil {
			t.Errorf("%s was stored", r.name)
		}
	}
	if _, err := db.ExecContext(ctx, ins, "c5", 4, 1, 6); err != nil {
		t.Errorf("seq 1 of the next epoch: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO command_results(command_id, machine_id, epoch, status, reason, message,
		result, received_time) VALUES ('c1', 'm1', 3, 'OK', '', '', x'00', 7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE command_results SET status = 'FAILED'`); err == nil {
		t.Error("a stored result was changed")
	}
	if _, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO command_results(command_id, machine_id, epoch, status,
		reason, message, result, received_time) VALUES ('c1', 'm1', 3, 'FAILED', '', '', x'00', 8)`); err == nil {
		t.Error("a stored result was replaced")
	}
}

// A store from migration 7 (the restore flag) gains the empty outbox.
func TestOpenMigratesAStoreFromTheRestoreSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:7]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if got := s.SchemaVersion(); got != 9 {
		t.Errorf("schema version %d, want 9", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"commands", "command_results"} {
		if n := rawCount(t, path, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s has %d rows", table, n)
		}
	}
}
