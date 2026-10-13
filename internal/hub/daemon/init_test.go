package daemon_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	_ "modernc.org/sqlite"
)

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestInitCreatesThePrivateStoreWithTheWholeIdentityRow(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)

	if got := perm(t, h.state); got != 0o700 {
		t.Errorf("state folder mode = %o, want 700", got)
	}
	db := filepath.Join(h.state, "state.db")
	if got := perm(t, db); got != 0o600 {
		t.Errorf("state.db mode = %o, want 600", got)
	}
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := raw.QueryRowContext(t.Context(), "SELECT count(*) FROM hub_identity").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if rows != 1 {
		t.Fatalf("hub_identity rows = %d, want 1", rows)
	}

	id := h.storedIdentity(t)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id.FleetID) {
		t.Errorf("fleet_id %q is not a random UUID", id.FleetID)
	}
	if len(id.AccountSalt) != 32 || id.SaltVersion != 1 || id.SettingsEpoch == "" || len(id.HubAddresses) != 0 {
		t.Errorf("salt %d bytes, salt_version %d, settings_epoch %q, hub_addresses %q; want 32, 1, set, empty",
			len(id.AccountSalt), id.SaltVersion, id.SettingsEpoch, id.HubAddresses)
	}
	caCert, err := x509.ParseCertificate(id.CACert)
	if err != nil {
		t.Fatal(err)
	}
	if !caCert.IsCA || caCert.PublicKeyAlgorithm != x509.Ed25519 {
		t.Errorf("CA IsCA %v key %v, want a CA with an Ed25519 key", caCert.IsCA, caCert.PublicKeyAlgorithm)
	}
	leaf, err := x509.ParseCertificate(id.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.PublicKeyAlgorithm != x509.Ed25519 {
		t.Errorf("server key %v, want Ed25519", leaf.PublicKeyAlgorithm)
	}
	if strings.Join(id.ServerNames, ",") != strings.Join(defaultNames, ",") {
		t.Errorf("stored SAN list %q, want %q", id.ServerNames, defaultNames)
	}
	for _, name := range defaultNames {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: caPool(t, id), DNSName: name, CurrentTime: time.Now()}); err != nil {
			t.Errorf("server certificate does not verify for %s: %v", name, err)
		}
	}

	// Never log key material: no form of the salt or the CA key appears.
	logs := h.logs.String()
	for _, secret := range [][]byte{id.AccountSalt, id.CAKey, id.ServerKey} {
		for _, form := range []string{hex.EncodeToString(secret), base64.StdEncoding.EncodeToString(secret), base64.RawURLEncoding.EncodeToString(secret)} {
			if strings.Contains(logs, form) {
				t.Error("a log line holds key material")
			}
		}
	}
	if len(h.logs.events(t, "hub.initialized")) != 1 {
		t.Error("no hub.initialized log line")
	}
}

func hashFile(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is a file in this test's own temp folder
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestASecondInitFailsAndChangesNothing(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	db := filepath.Join(h.state, "state.db")
	dbHash, cfgHash := hashFile(t, db), hashFile(t, h.config)
	err := daemon.Init(context.Background(), h.options())
	if err == nil {
		t.Fatal("a second init succeeded")
	}
	if got := reasonOf(t, err); got != daemon.ReasonStoreExists {
		t.Errorf("reason %q, want %q", got, daemon.ReasonStoreExists)
	}
	if hashFile(t, db) != dbHash || hashFile(t, h.config) != cfgHash {
		t.Error("the second init changed state.db or fleetd.yaml")
	}
	entries, err := os.ReadDir(h.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.db" && e.Name() != "fleetd.lock" && e.Name() != "approval_secret" {
			t.Errorf("the state folder holds %s after a refused init", e.Name())
		}
	}
}

// #92: init takes fleetd.lock too, so it cannot build a store under a
// running hub. The refusal is logged and counted.
func TestInitIsRefusedWhileTheLockIsHeld(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := lock.Acquire(filepath.Join(h.state, "fleetd.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()
	err = daemon.Init(context.Background(), h.options())
	if got := reasonOf(t, err); got != daemon.ReasonLockHeld {
		t.Errorf("reason %q, want %q", got, daemon.ReasonLockHeld)
	}
	if _, err := os.Stat(filepath.Join(h.state, "state.db")); !os.IsNotExist(err) {
		t.Errorf("state.db exists after a refused init: %v", err)
	}
	if got := sample(h.scrape(t), "fleet_hub_start_failures_total", `command="init"`, `reason="lock_held"`); got != 1 {
		t.Errorf("fleet_hub_start_failures_total{init,lock_held} = %v, want 1", got)
	}
	ev := h.logs.events(t, "hub.start_failed")
	if len(ev) != 1 || ev[0]["reason"] != "lock_held" || ev[0]["command"] != "init" {
		t.Errorf("hub.start_failed lines = %v, want one with command init and reason lock_held", ev)
	}
}

// A missing fleetd.yaml is written with the defaults (02j), and the store goes
// to the default state_dir under $HOME.
func TestInitWritesTheDefaultConfigWhenItIsMissing(t *testing.T) {
	home := shortDir(t)
	t.Setenv("HOME", home)
	logs := &lockedBuffer{}
	cfg := filepath.Join(home, ".config", "fleetd", "fleetd.yaml")
	err := daemon.Init(context.Background(), daemon.Options{
		ConfigPath: cfg,
		Hostname:   func() (string, error) { return testHost, nil },
		LogOutput:  logs,
		Version:    "test",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := perm(t, cfg); got != 0o600 {
		t.Errorf("fleetd.yaml mode = %o, want 600", got)
	}
	if got := perm(t, filepath.Join(home, ".local", "state", "fleetd", "state.db")); got != 0o600 {
		t.Errorf("state.db mode = %o, want 600", got)
	}
}
