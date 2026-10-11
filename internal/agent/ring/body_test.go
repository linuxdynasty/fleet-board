package ring_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Migration 5 adds command_log.command, a nullable BLOB, to the file itself.
func TestCommandColumnIsANullableBlob(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	open(t, dir)
	rows, err := raw(t, dir).QueryContext(context.Background(), `SELECT name, type, "notnull" FROM pragma_table_info('command_log')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var name, typ string
		var notNull int
		if err := rows.Scan(&name, &typ, &notNull); err != nil {
			t.Fatal(err)
		}
		if name == "command" {
			found = true
			if typ != "BLOB" || notNull != 0 {
				t.Errorf("command column is %s notnull=%d, want a nullable BLOB", typ, notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("command_log has no command column")
	}
}

// A command logged before migration 5 has no encoded command: the file
// migrates, and Open hands the entry back with a nil Body (the executor
// then ends it UNKNOWN_OUTCOME).
func TestAnEntryFromSchemaFourHasNoBody(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	if err := os.WriteFile(ring.Path(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := store.Open(ctx, store.Options{Path: ring.Path(dir), Name: "agent", Migrations: ring.Migrations()[:4], MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO command_log(command_id, kind, epoch, seq, received_time, not_after_time, status)
			VALUES ('old-1', 'CapturePane', 1, 1, 0, 60000, 'received')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db := open(t, dir)
	if got := db.SchemaVersion(); got != len(ring.Migrations()) {
		t.Errorf("schema version %d, want %d", got, len(ring.Migrations()))
	}
	entries, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "old-1" || entries[0].Status != ring.StatusReceived || entries[0].Body != nil {
		t.Errorf("open entries %+v, want old-1 received with a nil Body", entries)
	}
}

// Before the first HelloAck the stored epoch is 0, and no command is of the
// current epoch, not even one that says epoch 0.
func TestLogAndApplyBeforeAnyEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	if _, _, err := db.LogAndApply(ctx, command("c-0", 0, 1)); !errors.Is(err, ring.ErrWrongEpoch) {
		t.Errorf("LogAndApply at epoch 0: %v, want ErrWrongEpoch", err)
	}
	if _, err := db.Command(ctx, "c-0"); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Errorf("the refused command was logged (err %v)", err)
	}
}

// An entry needs an id and a kind, and a step is never negative; neither
// call saves anything.
func TestBadArgumentsSaveNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	if err := db.StartEpoch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	noID, noKind := command("", 1, 1), command("c-1", 1, 1)
	noKind.Kind = ""
	for _, c := range []ring.Command{noID, noKind} {
		if _, _, err := db.LogAndApply(ctx, c); err == nil {
			t.Errorf("LogAndApply(%+v) accepted it", c)
		}
		if _, _, err := db.LogCommand(ctx, c); err == nil {
			t.Errorf("LogCommand(%+v) accepted it", c)
		}
	}
	if st, _ := db.State(ctx); st.CommandSeq != 0 {
		t.Errorf("command seq %d after refused calls, want 0", st.CommandSeq)
	}
	if _, _, err := db.LogAndApply(ctx, command("c-2", 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunning(ctx, "c-2", -1); err == nil {
		t.Error("SetRunning accepted a negative step")
	}
	if e, _ := db.Command(ctx, "c-2"); e.Status != ring.StatusReceived {
		t.Errorf("after a refused SetRunning the entry is %s, want received", e.Status)
	}
}

// A known command_id is answered from its entry before any other check
// (02-contracts "Envelope checks"), even one whose re-issued seq agent.db
// could not store.
func TestAKnownIDIsAnsweredBeforeTheRangeCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	if err := db.StartEpoch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LogAndApply(ctx, command("k-1", 1, 1)); err != nil {
		t.Fatal(err)
	}
	e, dup, err := db.LogAndApply(ctx, command("k-1", 1, math.MaxUint64))
	if err != nil || !dup || e.Seq != 1 {
		t.Errorf("re-send with an out-of-range seq: entry %+v dup %v err %v, want the logged entry", e, dup, err)
	}
	if _, _, err := db.LogAndApply(ctx, command("k-2", 1, math.MaxUint64)); err == nil {
		t.Error("an unknown id with an out-of-range seq was accepted")
	}
}
