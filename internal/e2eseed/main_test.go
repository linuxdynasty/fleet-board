package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/seed"
	_ "modernc.org/sqlite" // the "sqlite" driver: pure Go, ADR 0014
)

// shortStateDir keeps the admin socket path under the OS limit; macOS
// t.TempDir paths can exceed it (the same reason internal/fleetcli avoids
// t.TempDir for hub state).
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fleet-e2eseed-") //nolint:usetesting // macOS socket limit, see above
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "state")
}

func writeConfig(t *testing.T, stateDir string) string {
	t.Helper()
	cfg := "state_dir: " + stateDir + "\nlisten: {agent: '127.0.0.1:0', api: '127.0.0.1:0'}\ndiscovery: {mdns: false}\n"
	path := filepath.Join(t.TempDir(), "fleetd.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func machineState(t *testing.T, dir, id string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var state string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM machines WHERE machine_id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("machine %s: %v", id, err)
	}
	return state
}

func TestRunInitsAndSeeds(t *testing.T) {
	t.Parallel()
	dir := shortStateDir(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{writeConfig(t, dir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != dir+"\n" {
		t.Fatalf("stdout = %q, want %q", got, dir+"\n")
	}
	for _, tc := range []struct {
		name, machine, want string
	}{
		{"laptop connected and snapshotted", seed.MachineLaptop, "ONLINE"},
		{"server enrolled but never connected", seed.MachineServer, "ENROLLED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := machineState(t, dir, tc.machine); got != tc.want {
				t.Errorf("machine %s state %q, want %q", tc.machine, got, tc.want)
			}
		})
	}
}

func TestRunRefusesASecondSeed(t *testing.T) {
	t.Parallel()
	dir := shortStateDir(t)
	cfg := writeConfig(t, dir)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{cfg}, &stdout, &stderr); code != 0 {
		t.Fatalf("first run = %d, stderr = %q", code, stderr.String())
	}
	if code := run(context.Background(), []string{cfg}, &stdout, &stderr); code == 0 {
		t.Fatal("second run = 0, want nonzero: the seed is never replaced")
	}
}

func TestRunNeedsExactlyOneArgument(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {}, {"a", "b"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("run(%q) = %d, want 2", args, code)
		}
	}
}
