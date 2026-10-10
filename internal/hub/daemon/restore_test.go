package daemon_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// #92: fleetd restore takes fleetd.lock; while another process holds it the
// restore is refused as lock_held, counted and logged with command
// restore, and state.db is untouched.
func TestRestoreIsRefusedWhileTheLockIsHeld(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	l, err := lock.Acquire(filepath.Join(h.state, daemon.LockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Release() })
	before, err := os.ReadFile(statedb.Path(h.state))
	if err != nil {
		t.Fatal(err)
	}

	_, err = daemon.Restore(context.Background(), h.options(), daemon.RestoreRequest{Backup: res.Path})
	if got := reasonOf(t, err); got != daemon.ReasonLockHeld || !strings.Contains(err.Error(), "stop fleetd run") {
		t.Errorf("Restore with the lock held = %v (%s), want lock_held naming fleetd run", err, got)
	}
	after, err := os.ReadFile(statedb.Path(h.state))
	if err != nil || !bytes.Equal(after, before) {
		t.Errorf("state.db changed under a held lock (%v)", err)
	}
	if got := sample(h.scrape(t), "fleet_hub_start_failures_total", `command="restore"`, `reason="lock_held"`); got != 1 {
		t.Errorf("fleet_hub_start_failures_total{command=restore,reason=lock_held} = %v, want 1", got)
	}
	if lines := h.logs.events(t, "hub.start_failed"); len(lines) != 1 || lines[0]["command"] != "restore" {
		t.Errorf("hub.start_failed lines %v, want one for command restore", lines)
	}
}

// fleetd run wants the server certificate names restore --address issued
// (tls names plus the host of every hub address), so it serves that
// certificate as is: no re-issue at start, and it covers the address.
func TestRunServesTheCertificateRestoreIssued(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Restore(context.Background(), h.options(), daemon.RestoreRequest{Backup: res.Path, Address: "Hub2.Example"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored := h.storedIdentity(t)
	if !slices.Equal(restored.HubAddresses, []string{"hub2.example:7443"}) {
		t.Errorf("hub_addresses %q, want hub2.example with the default agent port", restored.HubAddresses)
	}
	if done := h.logs.events(t, "restore.completed"); len(done) != 1 {
		t.Errorf("restore.completed lines %v, want one", done)
	}

	opts := h.options()
	opts.BackupTicks = make(chan time.Time)
	r := h.start(t, opts)
	st, err := dialAgent(t, caPool(t, restored), "127.0.0.1:"+port(r.addrs.Agent), "hub2.example", 0)
	if err != nil {
		t.Fatalf("TLS as hub2.example with the pinned CA: %v", err)
	}
	if !bytes.Equal(st.PeerCertificates[0].Raw, restored.ServerCert) {
		t.Error("fleetd run serves another certificate than the one restore issued")
	}
	if issued := h.logs.events(t, "tls.server_cert_issued"); len(issued) != 0 {
		t.Errorf("fleetd run re-issued the restored certificate: %v", issued)
	}
	r.stop(t)
}

// Restore sweeps a crashed restore's temporary file under the lock and logs
// hub.leftovers_removed for command restore.
func TestRestoreRemovesACrashedRestoresTemporaryFile(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(h.state, statedb.RestoreTempPrefix+"x")
	if err := os.WriteFile(left, []byte("half a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Restore(context.Background(), h.options(), daemon.RestoreRequest{Backup: res.Path}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Lstat(left); !os.IsNotExist(err) {
		t.Errorf("the crashed restore's temporary file is still there (%v)", err)
	}
	lines := h.logs.events(t, "hub.leftovers_removed")
	if len(lines) != 1 || lines[0]["command"] != "restore" || lines[0]["files"] != float64(1) {
		t.Errorf("hub.leftovers_removed lines %v, want one for command restore, 1 file", lines)
	}
}

// An --address without a port takes listen.agent's port.
func TestRestoreAddressTakesTheAgentListenerPort(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	body := "state_dir: " + h.state + "\nlisten: {agent: \"127.0.0.1:8443\", api: \"127.0.0.1:0\"}\n"
	if err := os.WriteFile(h.config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h.init(t)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Restore(context.Background(), h.options(), daemon.RestoreRequest{Backup: res.Path, Address: "hub2.example"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := h.storedIdentity(t).HubAddresses; !slices.Equal(got, []string{"hub2.example:8443"}) {
		t.Errorf("hub_addresses %q, want hub2.example:8443", got)
	}
}
