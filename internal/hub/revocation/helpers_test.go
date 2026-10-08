package revocation_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	_ "modernc.org/sqlite"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const header = "# fleet-board revoked-serials v1\n"

// serialRow is one cert_serials row to seed; revoked is zero when live.
type serialRow struct {
	serial   string
	notAfter time.Time
	revoked  time.Time
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fixture struct {
	dir   string
	store *store.Store
	logs  *lockedBuffer
}

// newFixture makes a state folder with a state.db holding rows, opens it,
// and writes the revoked-serials file when file is not nil.
func newFixture(t *testing.T, rows []serialRow, file *string) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet-1", CACert: []byte("ca"), CAKey: []byte("k"), ServerCert: []byte("s"), ServerKey: []byte("k"),
		ServerNames: []string{"hub"}, AccountSalt: bytes.Repeat([]byte{1}, 32), SaltVersion: 1, SettingsEpoch: "e", CreatedTime: now,
	}
	if err := statedb.Create(context.Background(), dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rows) > 0 {
		db, err := sql.Open("sqlite", statedb.Path(dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			var revoked any
			if !r.revoked.IsZero() {
				revoked = r.revoked.UnixMilli()
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO cert_serials(serial, machine_id, not_after_time, revoked_time, created_time)
				VALUES (?, 'm1', ?, ?, 1)`, r.serial, r.notAfter.UnixMilli(), revoked); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		if err := os.WriteFile(filepath.Join(dir, revocation.FileName), []byte(*file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := statedb.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &fixture{dir: dir, store: s, logs: &lockedBuffer{}}
}

func (f *fixture) load(t *testing.T, at time.Time) (*revocation.List, error) {
	t.Helper()
	return revocation.Load(context.Background(), revocation.Options{
		Dir: f.dir, Store: f.store, Now: func() time.Time { return at },
		Logger: slog.New(slog.NewJSONHandler(f.logs, nil)),
	})
}

func (f *fixture) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, revocation.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) revokedInStore(t *testing.T) []string {
	t.Helper()
	list, err := statedb.RevokedSerials(context.Background(), f.store, now)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range list {
		out = append(out, r.Serial)
	}
	return out
}

func line(serial string, notAfter time.Time) string {
	return serial + " " + notAfter.UTC().Format(time.RFC3339) + "\n"
}

func ptr(s string) *string { return &s }

func lines(parts ...string) string { return strings.Join(parts, "") }
