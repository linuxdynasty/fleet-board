package seed_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/protoversion"
	"github.com/linuxdynasty/fleet-board/internal/seed"
	"github.com/linuxdynasty/fleet-board/internal/store"
	_ "modernc.org/sqlite" // the "sqlite" driver: pure Go, ADR 0014
)

// The seed is versioned with the protos: a wire change must rebuild it.
func TestVersionFollowsTheWire(t *testing.T) {
	t.Parallel()
	if seed.Version != protoversion.Max {
		t.Errorf("seed.Version %d, want protoversion.Max %d", seed.Version, protoversion.Max)
	}
}

func seedDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	if err := seed.Seed(context.Background(), dir); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	db, err := store.OpenReadOnly(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func machineState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var state string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM machines WHERE machine_id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("machine %s: %v", id, err)
	}
	return state
}

// The shared fleet is one online laptop with a snapshot and one enrolled
// server that never connected, under a fixed fleet identity.
func TestSeedBuildsTheSharedFleet(t *testing.T) {
	t.Parallel()
	db := seedDB(t, filepath.Join(t.TempDir(), "state"))

	cases := []struct {
		name, machine, want string
	}{
		{"laptop connected and snapshotted", seed.MachineLaptop, "ONLINE"},
		{"server enrolled but never connected", seed.MachineServer, "ENROLLED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := machineState(t, db, tc.machine); got != tc.want {
				t.Errorf("machine %s state %q, want %q", tc.machine, got, tc.want)
			}
		})
	}

	var fleetID string
	if err := db.QueryRowContext(context.Background(), `SELECT fleet_id FROM hub_identity`).Scan(&fleetID); err != nil {
		t.Fatalf("hub_identity: %v", err)
	}
	if fleetID != seed.FleetID {
		t.Errorf("fleet_id %q, want %q", fleetID, seed.FleetID)
	}

	var facts string
	if err := db.QueryRowContext(context.Background(), `SELECT facts FROM machines WHERE machine_id = ?`, seed.MachineLaptop).Scan(&facts); err != nil {
		t.Fatalf("laptop facts: %v", err)
	}
	if facts != seed.LaptopFacts {
		t.Errorf("laptop facts %q, want %q", facts, seed.LaptopFacts)
	}
}

// Seeding twice must fail: the second run would otherwise silently replace
// the fleet every contract test shares.
func TestSeedTwiceFails(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	if err := seed.Seed(context.Background(), dir); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	if err := seed.Seed(context.Background(), dir); err == nil {
		t.Error("second Seed = nil, want an error")
	}
}
