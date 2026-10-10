package daemon_test

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedLeftLive writes, with the hub stopped, what a crash leaves: machineID
// ONLINE with a live machine_connections row no stream holds.
func (h *hub) seedLeftLive(t *testing.T, machineID string, at time.Time) {
	t.Helper()
	s, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `UPDATE machines SET state = 'ONLINE', last_epoch = 1
			WHERE machine_id = ?`, machineID); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), `INSERT INTO machine_connections(machine_id, epoch, boot_id,
			install_id, peer, connect_reason, started_time) VALUES (?, 1, 'boot-1', 'install-1', '127.0.0.1:1', 'start', ?)`,
			machineID, at.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// machineRow reads the machine's state and its epoch 1 end reason from the
// file while Run holds it (a plain reader; WAL lets it read).
func (h *hub) machineRow(t *testing.T, machineID string) (state, endReason string) {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(h.state))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var end sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT m.state, c.end_reason FROM machines m
		JOIN machine_connections c ON c.machine_id = m.machine_id AND c.epoch = 1 WHERE m.machine_id = ?`, machineID).
		Scan(&state, &end); err != nil {
		t.Fatal(err)
	}
	return state, end.String
}

// 03n "Hub start": before the agent listener serves, fleetd run closes the
// rows a crash left live (hub_restart) and makes every ONLINE machine STALE;
// the housekeeping tick then takes one that never comes back OFFLINE 3
// minutes after the start, by the hub clock.
func TestRunSweepsAtStartAndTheTickTakesASilentMachineOffline(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	first := h.start(t, h.options())
	res, _ := enrollOverRun(t, h, first)
	first.stop(t)
	id := res.GetMachineId().GetValue()
	start := time.Now()
	h.seedLeftLive(t, id, start.Add(-time.Hour))

	var now atomic.Int64
	now.Store(start.UnixNano())
	ticks := make(chan time.Time)
	opts := h.options()
	opts.Now = func() time.Time { return time.Unix(0, now.Load()) }
	opts.HousekeepingTicks = ticks
	h.start(t, opts)
	if state, end := h.machineRow(t, id); state != "STALE" || end != "hub_restart" {
		t.Fatalf("after the start: state %s, row end %q; want STALE and hub_restart", state, end)
	}
	// One line per start: the first run found nothing to do.
	if lines := h.logs.events(t, "hub.start_sweep"); len(lines) != 2 || lines[0]["stale"] != float64(0) ||
		lines[1]["stale"] != float64(1) || lines[1]["rows_closed"] != float64(1) {
		t.Errorf("hub.start_sweep lines %v", lines)
	}
	if lines := h.logs.events(t, "machine.state_changed"); len(lines) != 1 || lines[0]["reason"] != "hub_start" ||
		lines[0]["machine_id"] != id {
		t.Errorf("machine.state_changed lines %v", lines)
	}
	// 03n: fleet_hub_machine_state is set at every change, the sweep's too,
	// before any housekeeping pass.
	if v := sample(h.scrape(t), "fleet_hub_machine_state", `machine="`+id+`"`, `state="STALE"`); v != 1 {
		t.Errorf("fleet_hub_machine_state{state=STALE} = %v after the sweep, want 1", v)
	}
	tick := func(at time.Duration) {
		now.Store(start.Add(at).UnixNano())
		ticks <- time.Time{}
		ticks <- time.Time{} // the second send returns once the first check finished
	}
	tick(3*time.Minute - 10*time.Second)
	if state, _ := h.machineRow(t, id); state != "STALE" {
		t.Fatalf("state %s just under 3 minutes after the start, want STALE", state)
	}
	tick(3 * time.Minute)
	if state, _ := h.machineRow(t, id); state != "OFFLINE" {
		t.Errorf("state %s 3 minutes after the start, want OFFLINE", state)
	}
}
