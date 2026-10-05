package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// ADR 0014 pragmas, as SQLite reports them back: synchronous FULL is 2 and
// journal_size_limit is 64 MiB.
var wantPragmas = []struct {
	name           string
	writer, reader string
}{
	{"journal_mode", "wal", "wal"},
	{"synchronous", "2", "2"},
	{"fullfsync", "1", "1"},
	{"busy_timeout", "5000", "5000"},
	{"foreign_keys", "1", "1"},
	{"journal_size_limit", "67108864", "67108864"},
	{"query_only", "0", "1"},
}

func pragma(tx *sql.Tx, name string) (string, error) {
	var v string
	err := tx.QueryRow("PRAGMA " + name).Scan(&v)
	return v, err
}

func TestEveryConnectionHasTheADR0014Pragmas(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	ctx := context.Background()
	for _, p := range wantPragmas {
		t.Run(p.name, func(t *testing.T) {
			var w, r string
			if err := s.Write(ctx, func(tx *sql.Tx) (err error) { w, err = pragma(tx, p.name); return err }); err != nil {
				t.Fatalf("writer: %v", err)
			}
			if err := s.Read(ctx, func(tx *sql.Tx) (err error) { r, err = pragma(tx, p.name); return err }); err != nil {
				t.Fatalf("reader: %v", err)
			}
			if w != p.writer || r != p.reader {
				t.Errorf("PRAGMA %s: writer %q reader %q, want %q and %q", p.name, w, r, p.writer, p.reader)
			}
		})
	}
}

func TestTheDatabaseFileIsInWALMode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := rawQueryString(t, h.path, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("journal_mode in the file = %q, want wal", got)
	}
}

// ADR 0015: logs.db adds auto_vacuum=INCREMENTAL (2), set before any table
// exists; the other files keep the default NONE (0).
func TestOnlyLogsDBUsesIncrementalAutoVacuum(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, want string }{
		{"state", "0"},
		{"logs", "2"},
		{"agent", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), tc.name+".db")
			if err := openClose(t, store.Options{Path: path, Name: tc.name, Migrations: []store.Migration{itemsV1}}); err != nil {
				t.Fatalf("open: %v", err)
			}
			if got := rawQueryString(t, path, "PRAGMA auto_vacuum"); got != tc.want {
				t.Errorf("auto_vacuum in %s.db = %s, want %s", tc.name, got, tc.want)
			}
		})
	}
}

// The writer takes the write lock at BEGIN (BEGIN IMMEDIATE), so a job that
// has not written anything yet already keeps every other writer out. With a
// deferred BEGIN the other connection would get the lock.
func TestTheWriterHoldsTheWriteLockFromBegin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	started, release := make(chan struct{}), make(chan struct{})
	held, err := s.Submit(context.Background(), func(*sql.Tx) error { close(started); <-release; return nil })
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started

	db, err := sql.Open("sqlite", h.path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("raw conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	var se *sqlite.Error
	if !errors.As(err, &se) || se.Code()&0xff != sqlite3.SQLITE_BUSY {
		if err == nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
		t.Errorf("another connection's BEGIN IMMEDIATE while a job is held: %v, want SQLITE_BUSY", err)
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatalf("held job: %v", err)
	}
}

func TestReadersCannotWrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	err := s.Read(context.Background(), func(tx *sql.Tx) error { return insert("r")(tx) })
	if err == nil {
		t.Fatal("an INSERT on a reader succeeded; readers must be query_only")
	}
}

func TestOpenRejectsBadOptions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ok := store.Options{Path: filepath.Join(dir, "state.db"), Name: "state"}
	cases := []struct {
		name   string
		change func(o *store.Options)
	}{
		{"no path", func(o *store.Options) { o.Path = "" }},
		{"path with a query", func(o *store.Options) { o.Path = filepath.Join(dir, "x.db?mode=memory") }},
		{"unknown name", func(o *store.Options) { o.Name = "cache" }},
		{"no name", func(o *store.Options) { o.Name = "" }},
		{"batch over 100", func(o *store.Options) { o.BatchMax = 101 }},
		{"negative batch", func(o *store.Options) { o.BatchMax = -1 }},
		{"negative window", func(o *store.Options) { o.BatchWindow = -time.Millisecond }},
		{"readers over 10", func(o *store.Options) { o.Readers = 11 }},
		{"negative queue", func(o *store.Options) { o.QueueSize = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := ok
			tc.change(&o)
			if s, err := store.Open(context.Background(), o); err == nil {
				_ = s.Close()
				t.Fatalf("Open(%+v) succeeded, want an error", o)
			}
		})
	}
}

func TestStoreSizeIsExported(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if err := s.Write(context.Background(), insert("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body := h.scrape(t)
	for _, name := range []string{"fleet_hub_store_bytes", "fleet_hub_store_wal_bytes"} {
		if got := sample(body, name, `db="state"`); got <= 0 {
			t.Errorf("%s{db=state} = %v, want > 0\n%s", name, got, fmt.Sprint(len(body)))
		}
	}
}
