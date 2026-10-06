package statedb_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	_ "modernc.org/sqlite"
)

func sampleIdentity() statedb.Identity {
	return statedb.Identity{
		FleetID:       "6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f60",
		CACert:        []byte("ca-cert-der"),
		CAKey:         []byte("ca-key-der"),
		ServerCert:    []byte("server-cert-der"),
		ServerKey:     []byte("server-key-der"),
		ServerNames:   []string{"hub", "hub.local", "localhost", "127.0.0.1", "::1"},
		AccountSalt:   bytes.Repeat([]byte{7}, 32),
		SaltVersion:   1,
		SettingsEpoch: "0b9d3c1e-6a2f-4e8d-8c7b-5a4f3e2d1c0b",
		CreatedTime:   time.UnixMilli(1_790_000_000_000),
	}
}

// stateDir makes a fresh 0700 state folder.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	return dir
}

func create(t *testing.T, dir string) {
	t.Helper()
	if err := statedb.Create(context.Background(), dir, sampleIdentity(), statedb.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func rawCount(t *testing.T, path, query string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func open(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := statedb.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEnsureDirCreatesA0700Folder(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	if got := mode(t, dir); got != 0o700 {
		t.Errorf("state folder mode = %o, want 700", got)
	}
}

func TestEnsureDirRefusesALooseFolderAndLeavesItAlone(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := statedb.EnsureDir(dir); !errors.Is(err, statedb.ErrLooseMode) {
		t.Errorf("EnsureDir on a 0755 folder = %v, want ErrLooseMode", err)
	}
	if got := mode(t, dir); got != 0o755 {
		t.Errorf("EnsureDir changed the folder mode to %o", got)
	}
}

func TestCreateWritesA0600StateDBWithExactlyOneIdentityRowAndNothingElse(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	path := filepath.Join(dir, "state.db")
	if got := mode(t, path); got != 0o600 {
		t.Errorf("state.db mode = %o, want 600", got)
	}
	if n := rawCount(t, path, "SELECT count(*) FROM hub_identity"); n != 1 {
		t.Errorf("hub_identity rows = %d, want 1", n)
	}
	if got := listDir(t, dir); !reflect.DeepEqual(got, []string{"state.db"}) {
		t.Errorf("state folder holds %q, want only state.db (no temp file, -wal or -shm)", got)
	}
	got, err := statedb.ReadIdentity(context.Background(), open(t, dir))
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	want := sampleIdentity()
	want.HubAddresses = []string{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadIdentity = %+v\nwant %+v", got, want)
	}
}

func TestTheSchemaAllowsOneIdentityRowOnly(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`INSERT INTO hub_identity(id, fleet_id, ca_cert, ca_key, server_cert, server_key,
		server_names, account_salt, salt_version, settings_epoch, created_time)
		VALUES (2, 'x', x'00', x'00', x'00', x'00', '[]', zeroblob(32), 1, 'e', 0)`)
	if err == nil {
		t.Error("a second hub_identity row was accepted")
	}
}

func TestCreateRefusesAnExistingStateDBAndChangesNothing(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	path := filepath.Join(dir, "state.db")
	before, files := fileHash(t, path), listDir(t, dir)
	other := sampleIdentity()
	other.FleetID = "another-fleet"
	err := statedb.Create(context.Background(), dir, other, statedb.CreateOptions{})
	if !errors.Is(err, statedb.ErrExists) {
		t.Fatalf("second Create = %v, want ErrExists", err)
	}
	if fileHash(t, path) != before {
		t.Error("the second Create changed state.db")
	}
	if got := listDir(t, dir); !reflect.DeepEqual(got, files) {
		t.Errorf("state folder now holds %q, want %q", got, files)
	}
}

// The rename is the last step; a failure before it leaves no state.db and
// no temporary file.
func TestAFailureBeforeTheLinkLeavesNoStateDB(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	boom := errors.New("injected link failure")
	err := statedb.Create(context.Background(), dir, sampleIdentity(), statedb.CreateOptions{
		Link: func(string, string) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Create = %v, want the injected failure", err)
	}
	if got := listDir(t, dir); len(got) != 0 {
		t.Errorf("state folder holds %q after a failed Create, want nothing", got)
	}
}

func TestOpenRefusesAMissingStateDBAndCreatesNone(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	s, err := statedb.Open(context.Background(), dir, nil)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded without state.db")
	}
	if !errors.Is(err, store.ErrMissing) {
		t.Errorf("Open = %v, want store.ErrMissing", err)
	}
	if got := listDir(t, dir); len(got) != 0 {
		t.Errorf("Open left %q in the state folder", got)
	}
}

func TestOpenRefusesGroupOrOtherAccess(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		dirMode, file os.FileMode
	}{
		{"folder group readable", 0o750, 0o600},
		{"folder other executable", 0o701, 0o600},
		{"state.db group readable", 0o700, 0o640},
		{"state.db other readable", 0o700, 0o604},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := stateDir(t)
			create(t, dir)
			if err := os.Chmod(filepath.Join(dir, "state.db"), tc.file); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.dirMode); err != nil {
				t.Fatal(err)
			}
			s, err := statedb.Open(context.Background(), dir, nil)
			if err == nil {
				_ = s.Close()
				t.Fatal("Open accepted a loose mode")
			}
			if !errors.Is(err, statedb.ErrLooseMode) {
				t.Errorf("Open = %v, want ErrLooseMode", err)
			}
		})
	}
}

func TestReadIdentityOnAStoreWithoutTheRowIsErrNoIdentity(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, "state.db")
	bare, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	if err := bare.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.ReadIdentity(context.Background(), open(t, dir)); !errors.Is(err, statedb.ErrNoIdentity) {
		t.Errorf("ReadIdentity = %v, want ErrNoIdentity", err)
	}
}

func TestReplaceServerCertSwapsOnlyFromTheCertificateItWasGiven(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	names := []string{"new.example", "127.0.0.1"}
	if err := statedb.ReplaceServerCert(ctx, s, []byte("stale"), []byte("c2"), []byte("k2"), names); err == nil {
		t.Error("ReplaceServerCert accepted an old certificate that is not the stored one")
	}
	if err := statedb.ReplaceServerCert(ctx, s, []byte("server-cert-der"), []byte("c2"), []byte("k2"), names); err != nil {
		t.Fatalf("ReplaceServerCert: %v", err)
	}
	got, err := statedb.ReadIdentity(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleIdentity()
	if string(got.ServerCert) != "c2" || string(got.ServerKey) != "k2" || !reflect.DeepEqual(got.ServerNames, names) {
		t.Errorf("server fields = %q %q %q", got.ServerCert, got.ServerKey, got.ServerNames)
	}
	if !bytes.Equal(got.CACert, want.CACert) || !bytes.Equal(got.CAKey, want.CAKey) || got.FleetID != want.FleetID ||
		!bytes.Equal(got.AccountSalt, want.AccountSalt) || got.SettingsEpoch != want.SettingsEpoch {
		t.Error("ReplaceServerCert changed a field other than the server certificate")
	}
}
