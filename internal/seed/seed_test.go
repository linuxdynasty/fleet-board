package seed_test

import (
	"context"
	"database/sql"
	"fmt"
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

// Two independent seeds hold the same rows: the fixed clock and the fixed
// enrollment order leave no run-to-run variation for golden tests to trip
// on.
func TestTwoSeedsHoldTheSameRows(t *testing.T) {
	t.Parallel()
	dumpQuery := func(t *testing.T, db *sql.DB, q string) string {
		t.Helper()
		rows, err := db.QueryContext(context.Background(), q)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		defer func() { _ = rows.Close() }()
		cols, err := rows.Columns()
		if err != nil {
			t.Fatalf("columns %q: %v", q, err)
		}
		var out string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %q: %v", q, err)
			}
			out += q + "\n"
			for i, c := range cols {
				out += c + "=" + fmt.Sprintf("%v", vals[i]) + "\n"
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows %q: %v", q, err)
		}
		return out
	}
	dump := func(t *testing.T, dir string) string {
		t.Helper()
		db := seedDB(t, dir)
		return dumpQuery(t, db,
			`SELECT machine_id, name, expected_hostname, labels, install_id, state, facts, created_time FROM machines ORDER BY machine_id`) +
			dumpQuery(t, db,
				`SELECT action, target, machine_id, create_time, hub_seq FROM audit_log ORDER BY hub_seq`)
	}
	a := dump(t, filepath.Join(t.TempDir(), "a"))
	b := dump(t, filepath.Join(t.TempDir(), "b"))
	if a != b {
		t.Errorf("two seeds differ:\n--- a\n%s\n--- b\n%s", a, b)
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
