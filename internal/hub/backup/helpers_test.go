package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// testWorkFactor keeps age's scrypt cheap in tests; production uses age's
// default.
const testWorkFactor = 10

// stamp is the fixed clock of most tests: 2026-10-08 03:00:00 UTC.
var stamp = time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)

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

// events returns the decoded log lines whose msg is name.
func (b *lockedBuffer) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(strings.NewReader(b.String()))
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
}

// newFixture makes a state folder (0700) holding a state.db with a
// hub_identity row whose fleet id is "fleet-test".
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := emptyFixture(t)
	id := statedb.Identity{
		FleetID: "fleet-test", CACert: []byte("ca"), CAKey: []byte("ca-key"),
		ServerCert: []byte("cert"), ServerKey: []byte("key"), ServerNames: []string{"localhost"},
		AccountSalt: bytes.Repeat([]byte{7}, 32), SaltVersion: 1, SettingsEpoch: "epoch",
		CreatedTime: stamp,
	}
	if err := statedb.Create(context.Background(), f.state, id, statedb.CreateOptions{}); err != nil {
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

func (f *fixture) backups() string { return filepath.Join(f.state, "backups") }

// names lists a folder, sorted; a missing folder, or a file in its place, is
// empty.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// assertBackupStore checks a plaintext copy: it opens read-only, passes
// quick_check and holds the fixture's hub_identity row.
func assertBackupStore(t *testing.T, path string) {
	t.Helper()
	db, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	defer func() { _ = db.Close() }()
	var check, fleet string
	if err := db.QueryRowContext(t.Context(), "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Errorf("quick_check = %q, %v; want ok", check, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT fleet_id FROM hub_identity WHERE id = 1").Scan(&fleet); err != nil || fleet != "fleet-test" {
		t.Errorf("hub_identity fleet_id = %q, %v; want fleet-test", fleet, err)
	}
}

// fakeTerminal answers ReadPassword with answers in order.
type fakeTerminal struct {
	terminal bool
	answers  []string
	prompts  []string
}

func (f *fakeTerminal) IsTerminal() bool { return f.terminal }

func (f *fakeTerminal) ReadPassword(prompt string) ([]byte, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.answers) == 0 {
		return nil, errors.New("no more input")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return []byte(a), nil
}

// plaintextLeft lists files in the state folder other than state.db, its
// SQLite side files and the backups folder: a plaintext intermediate would
// show up here. With the hub stopped, the read-only copy connection leaves
// SQLite's state.db-wal and state.db-shm behind; they must be 0600.
func plaintextLeft(t *testing.T, state string) []string {
	t.Helper()
	var out []string
	for _, n := range names(t, state) {
		switch n {
		case "state.db", "backups":
		case "state.db-wal", "state.db-shm":
			if m := mode(t, filepath.Join(state, n)); m != 0o600 {
				t.Errorf("%s mode %04o, want 0600", n, m)
			}
		default:
			out = append(out, n)
		}
	}
	return out
}
