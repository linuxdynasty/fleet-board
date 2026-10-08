package main_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// backupCmd runs `fleetd backup` with stdin and returns stdout, stderr and
// the exit code.
func backupCmd(t *testing.T, config, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := fleetd(t, append([]string{"backup", "--config", config}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), exitCode(err)
}

// Plan row 3 (#92): `fleetd backup` works while `fleetd run` holds
// fleetd.lock and leaves the lock with the hub. Stdout holds only the path
// of a mode 0600 copy that holds the hub's CA.
func TestBackupRunsBesideARunningHub(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	if out, err := fleetd(t, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	ca := storedCA(t, state)
	hub, _ := startRun(t, config)
	stdout, stderr, code := backupCmd(t, config, "")
	if code != 0 {
		t.Fatalf("fleetd backup beside a running hub: exit %d\n%s", code, stderr)
	}
	path := strings.TrimSuffix(stdout, "\n")
	if strings.Contains(path, "\n") || filepath.Dir(path) != filepath.Join(state, "backups") ||
		!strings.HasPrefix(filepath.Base(path), "manual-") || !strings.HasSuffix(path, ".db") {
		t.Fatalf("stdout %q, want one line naming backups/manual-*.db", stdout)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o, want 0600", fi.Mode().Perm())
	}
	second, err := fleetd(t, "run", "--config", config).CombinedOutput()
	if exitCode(err) != 1 || !strings.Contains(string(second), "lock_held") {
		t.Errorf("a second fleetd run after the backup: exit %d, want 1 with lock_held:\n%s", exitCode(err), second)
	}
	stopWith(t, hub, syscall.SIGTERM)
	db, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var got []byte
	if err := db.QueryRowContext(t.Context(), "SELECT ca_cert FROM hub_identity WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("the backup has no hub_identity: %v", err)
	}
	if !bytes.Equal(got, ca) {
		t.Error("the backup's CA is not the hub's")
	}
}

// Plan row 3: --encrypt refuses a stdin that is not a terminal, even one
// that holds a passphrase, and writes nothing. There is no passphrase flag.
func TestBackupEncryptRefusesAPipeAndHasNoPassphraseFlag(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	if out, err := fleetd(t, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	stdout, stderr, code := backupCmd(t, config, "secret\nsecret\n", "--encrypt")
	if code != 1 || stdout != "" {
		t.Errorf("fleetd backup --encrypt from a pipe: exit %d, stdout %q; want 1 and nothing", code, stdout)
	}
	if !strings.Contains(stderr, "not one") || !strings.Contains(stderr, `"step":"passphrase"`) {
		t.Errorf("stderr does not explain the refusal or log backup.failed at step passphrase:\n%s", stderr)
	}
	if strings.Contains(stderr, "secret") {
		t.Error("stderr echoes the piped passphrase")
	}
	if entries, err := os.ReadDir(filepath.Join(state, "backups")); err == nil && len(entries) > 0 {
		t.Errorf("backups/ holds %d files after the refusal", len(entries))
	}
	if _, _, code := backupCmd(t, config, "", "--encrypt", "--passphrase", "x"); code != 2 {
		t.Errorf("--passphrase exit %d, want 2 (no such flag)", code)
	}
}
