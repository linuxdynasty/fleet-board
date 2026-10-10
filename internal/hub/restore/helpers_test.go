package restore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/restore"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	_ "modernc.org/sqlite"
)

// now is the fixed clock of the tests.
var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// events returns the decoded log lines whose msg is name.
func (b *lockedBuffer) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	text := b.buf.String()
	b.mu.Unlock()
	var out []map[string]any
	dec := json.NewDecoder(strings.NewReader(text))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

type fixture struct {
	state string
	tel   *telemetry.Telemetry
	logs  *lockedBuffer
	id    statedb.Identity
}

// newFixture makes a state folder whose state.db holds a hub identity with
// a real CA for fleet, and returns it with the identity it wrote.
func newFixture(t *testing.T, fleet string) *fixture {
	t.Helper()
	f := emptyFixture(t)
	auth, err := ca.New(now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.KeyDER()
	if err != nil {
		t.Fatal(err)
	}
	sc, err := auth.IssueServer([]string{"hub", "localhost"}, now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.id = statedb.Identity{
		FleetID: fleet, CACert: auth.Cert.Raw, CAKey: key, ServerCert: sc.CertDER, ServerKey: sc.KeyDER,
		ServerNames: sc.Names, AccountSalt: bytes.Repeat([]byte{7}, 32), SaltVersion: 1, SettingsEpoch: "epoch-" + fleet,
		HubAddresses: []string{}, CreatedTime: now,
	}
	if err := statedb.Create(context.Background(), f.state, f.id, statedb.CreateOptions{}); err != nil {
		t.Fatalf("statedb.Create: %v", err)
	}
	return f
}

// emptyFixture is a private state folder with no state.db.
func emptyFixture(t *testing.T) *fixture {
	t.Helper()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	state := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(state); err != nil {
		t.Fatal(err)
	}
	return &fixture{state: state, tel: tel, logs: logs}
}

// backupOf takes a manual backup of the fixture's state.db and returns its
// path.
func (f *fixture) backupOf(t *testing.T) string {
	t.Helper()
	res, err := backup.Take(context.Background(), backup.Options{StateDir: f.state, Trigger: backup.Manual, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("backup.Take: %v", err)
	}
	return res.Path
}

// options restores path into f with the test clock and the fixture's logs.
func (f *fixture) options(path string) restore.Options {
	return restore.Options{
		StateDir: f.state, Backup: path, Now: func() time.Time { return now }, Telemetry: f.tel,
		Names: func(addrs []string) ([]string, error) {
			return append([]string{"hub", "localhost"}, hosts(addrs)...), nil
		},
	}
}

func hosts(addrs []string) []string {
	var out []string
	for _, a := range addrs {
		h, err := restore.Host(a)
		if err != nil {
			panic(err)
		}
		out = append(out, h)
	}
	return out
}

// identity opens the fixture's state.db and reads its identity.
func (f *fixture) identity(t *testing.T) statedb.Identity {
	t.Helper()
	s, err := statedb.Open(context.Background(), f.state, nil)
	if err != nil {
		t.Fatalf("open the restored state.db: %v", err)
	}
	defer func() { _ = s.Close() }()
	id, err := statedb.ReadIdentity(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// execSQL runs one statement on path with a plain connection.
func execSQL(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// count runs a count query on path read-only.
func count(t *testing.T, path, query string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// copyFile copies src to dst with mode 0600; a missing src copies nothing.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src) //nolint:gosec // G304: a file in the test's temp folder
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a file in the test's temp folder
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// previous lists the kept copies of the old state.db.
func previous(t *testing.T, state string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(state, "state.db.pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
