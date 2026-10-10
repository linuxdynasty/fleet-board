package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// restoreCmd runs `fleetd restore` and returns stdout, stderr and the exit
// code.
func restoreCmd(t *testing.T, config string, args ...string) (string, string, int) {
	t.Helper()
	cmd := fleetd(t, append([]string{"restore", "--config", config}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), exitCode(err)
}

// initAndBackup runs fleetd init and fleetd backup and returns the path of
// the copy.
func initAndBackup(t *testing.T, config string) string {
	t.Helper()
	if out, err := fleetd(t, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	stdout, stderr, code := backupCmd(t, config, "")
	if code != 0 {
		t.Fatalf("fleetd backup: exit %d\n%s", code, stderr)
	}
	return strings.TrimSuffix(stdout, "\n")
}

func identity(t *testing.T, state string) statedb.Identity {
	t.Helper()
	s, err := statedb.Open(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, err := statedb.ReadIdentity(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// previousCopies lists the state.db.pre-restore-* copies in the state folder.
func previousCopies(t *testing.T, state string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(state, "state.db.pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Plan row 5 (#92): fleetd restore takes fleetd.lock, so it is refused while
// fleetd run holds it; the hub keeps serving.
func TestRestoreIsRefusedWhileTheHubRuns(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	path := initAndBackup(t, config)
	hub, apiPort := startRun(t, config)
	stdout, stderr, code := restoreCmd(t, config, path)
	if code != 1 || stdout != "" {
		t.Errorf("restore beside a running hub: exit %d, stdout %q; want 1 and nothing", code, stdout)
	}
	if !strings.Contains(stderr, "lock_held") || !strings.Contains(stderr, "stop fleetd run") {
		t.Errorf("stderr does not explain the refusal (lock_held, stop fleetd run):\n%s", stderr)
	}
	if got := previousCopies(t, state); len(got) != 0 {
		t.Errorf("a refused restore kept a previous copy: %v", got)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+apiPort+"/metrics", nil)
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatalf("the hub stopped serving after the refused restore: %v", err)
	}
	_ = resp.Body.Close()
	stopWith(t, hub, syscall.SIGTERM)
}

// Plan row 5's acceptance: after a restore, fleetd run serves a certificate
// signed by the same CA (the one agents pinned), covering the --address
// given; the fleet id is kept, settings_epoch is new and the 24 h
// reconcile window is set. A second restore works the same way and keeps a
// second previous copy.
func TestRestoreThenRunServesTheSameCA(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	path := initAndBackup(t, config)
	before := identity(t, state)

	start := time.Now()
	stdout, stderr, code := restoreCmd(t, config, path, "--address", "Hub2.Example")
	if code != 0 {
		t.Fatalf("fleetd restore: exit %d\n%s", code, stderr)
	}
	prev := previousCopies(t, state)
	if len(prev) != 1 || !strings.Contains(stdout, prev[0]) || !strings.Contains(stdout, "fleetd run") {
		t.Errorf("stdout %q; want the previous copy (%v) and the next step named", stdout, prev)
	}
	if fi, err := os.Stat(prev[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("previous copy mode %v (%v), want 0600", fi.Mode(), err)
	}
	after := identity(t, state)
	if after.FleetID != before.FleetID || !bytes.Equal(after.CACert, before.CACert) || !bytes.Equal(after.CAKey, before.CAKey) ||
		!bytes.Equal(after.AccountSalt, before.AccountSalt) {
		t.Error("the restore changed the fleet id, the CA or the salt")
	}
	if after.SettingsEpoch == before.SettingsEpoch || after.SettingsEpoch == "" {
		t.Errorf("settings_epoch %q after the restore, want a new one (the backup's was %q)", after.SettingsEpoch, before.SettingsEpoch)
	}
	if want := []string{"hub2.example:7443"}; !slices.Equal(after.HubAddresses, want) {
		t.Errorf("hub_addresses %q, want %q", after.HubAddresses, want)
	}
	lo, hi := start.Add(24*time.Hour-time.Second), time.Now().Add(24*time.Hour+time.Second)
	if u := after.RestoreReconcileUntil; u.Before(lo) || u.After(hi) {
		t.Errorf("restore_reconcile_until %v, want 24 h after the restore (%v to %v)", u, lo, hi)
	}

	hub, p := startRunPorts(t, config)
	leaf, root := dialAgentListener(t, p.agent, before.CACert, "hub2.example")
	if fp, want := sha256.Sum256(root), sha256.Sum256(before.CACert); fp != want {
		t.Error("the restored hub serves a chain whose CA is not the one agents pinned")
	}
	for _, name := range []string{"hub2.example", "localhost"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Errorf("the served certificate does not cover %s: %v", name, err)
		}
	}
	stopWith(t, hub, syscall.SIGTERM)

	_, stderr, code = restoreCmd(t, config, path)
	if code != 0 {
		t.Fatalf("second fleetd restore: exit %d\n%s", code, stderr)
	}
	again := identity(t, state)
	if again.SettingsEpoch == after.SettingsEpoch || again.SettingsEpoch == before.SettingsEpoch {
		t.Errorf("settings_epoch %q after the second restore, want one unlike %q and %q", again.SettingsEpoch, after.SettingsEpoch, before.SettingsEpoch)
	}
	if len(again.HubAddresses) != 0 {
		t.Errorf("hub_addresses %q after restoring the backup without --address, want the backup's (none)", again.HubAddresses)
	}
	if got := previousCopies(t, state); len(got) != 2 {
		t.Errorf("previous copies %v after two restores, want 2", got)
	}
	hub, p = startRunPorts(t, config)
	dialAgentListener(t, p.agent, before.CACert, "localhost")
	stopWith(t, hub, syscall.SIGTERM)
}

// dialAgentListener makes a TLS 1.3 handshake with the agent listener the
// way an enrolled agent checks it: the pinned CA as the only root, for
// name. It returns the leaf and the CA the hub sent in the chain.
func dialAgentListener(t *testing.T, port string, caDER []byte, name string) (*x509.Certificate, []byte) {
	t.Helper()
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{
		RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS13,
	}}
	c, err := d.DialContext(t.Context(), "tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("TLS to the agent listener as %s with the pinned CA: %v", name, err)
	}
	defer func() { _ = c.Close() }()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates //nolint:forcetypeassert // tls.Dialer returns *tls.Conn
	if len(certs) != 2 {
		t.Fatalf("the hub sent %d certificates, want the leaf and the CA", len(certs))
	}
	return certs[0], certs[1].Raw
}

// A corrupt, encrypted, too-new or foreign backup, or a bad --address, is
// refused with its own message, and state.db and its WAL are byte for byte
// what they were. No previous copy is kept.
func TestRestoreRefusesABadBackupAndChangesNothing(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	good := initAndBackup(t, config)
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o600); err != nil { //nolint:gosec // G703: a fixed name in the test's temp folder
			t.Fatal(err)
		}
		return p
	}
	raw, err := os.ReadFile(good) //nolint:gosec // G304: the backup this test made
	if err != nil {
		t.Fatal(err)
	}
	tooNew := write("too-new.db", raw)
	addVersion(t, tooNew, 99)
	otherConfig, _ := setup(t)
	foreign := initAndBackup(t, otherConfig)

	cases := []struct {
		name, want string
		args       []string
		code       int
	}{
		{"not a database", "not a usable backup", []string{write("junk.db", bytes.Repeat([]byte("junk"), 2048))}, 1},
		{"truncated", "not a usable backup", []string{write("half.db", raw[:len(raw)/2])}, 1},
		{"encrypted", "encrypted", []string{write("x.db.age", []byte("age-encryption.org/v1\n-> scrypt abc 18\n"))}, 1},
		{"schema too new", "schema version", []string{tooNew}, 1},
		{"another fleet", "another fleet", []string{foreign}, 1},
		{"missing file", "no such file", []string{filepath.Join(dir, "none.db")}, 1},
		{"bad address", "address", []string{good, "--address", "bad host!"}, 1},
		{"no backup named", "", nil, 2},
	}
	for _, tc := range cases {
		db, wal := fileBytes(t, statedb.Path(state)), fileBytes(t, statedb.Path(state)+"-wal")
		stdout, stderr, code := restoreCmd(t, config, tc.args...)
		if code != tc.code || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d, stdout %q; want %d, nothing, and %q in stderr:\n%s", tc.name, code, stdout, tc.code, tc.want, stderr)
		}
		if !bytes.Equal(fileBytes(t, statedb.Path(state)), db) || !bytes.Equal(fileBytes(t, statedb.Path(state)+"-wal"), wal) {
			t.Errorf("%s: state.db or its WAL changed", tc.name)
		}
		if got := previousCopies(t, state); len(got) != 0 {
			t.Errorf("%s: kept a previous copy %v", tc.name, got)
		}
		if tc.code == 1 && !strings.Contains(stderr, `"msg":"restore.failed"`) && !strings.Contains(stderr, `"msg":"hub.start_failed"`) {
			t.Errorf("%s: no restore.failed log line:\n%s", tc.name, stderr)
		}
	}
}

// fileBytes is the file's content, nil when it does not exist.
func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's state folder
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

// addVersion records a schema version this fleetd does not know.
func addVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO schema_migrations(version, name, checksum, applied_time) VALUES (?, 'future', 'x', 0)", v); err != nil {
		t.Fatal(err)
	}
}
