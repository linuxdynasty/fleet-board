package daemon_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedCommands writes, with the hub stopped and through the outbox's own
// calls, c-due (pending, due at due) and c-old (expired at closed).
func (h *hub) seedCommands(t *testing.T, machineID string, due, closed time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := statedb.Open(ctx, h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cmd := func(id string, at time.Time) *agentv1.Command {
		return &agentv1.Command{
			CommandId: &typev1.CommandId{Value: id}, NotAfterTime: timestamppb.New(at),
			Kind: &agentv1.Command_Drain{Drain: &agentv1.Drain{}},
		}
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: machineID, Command: cmd("c-old", closed),
			Now: closed.Add(-time.Minute),
		}); err != nil {
			return err
		}
		if _, err := outbox.Expire(ctx, tx, machineID, closed, 10); err != nil {
			return err
		}
		_, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: machineID, Command: cmd("c-due", due),
			Now: due.Add(-time.Minute),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// commandState is the state of command id in the file, "" when it is gone.
func (h *hub) commandState(t *testing.T, id string) string {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(h.state))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var state string
	err = db.QueryRowContext(context.Background(), `SELECT state FROM commands WHERE command_id = ?`, id).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return state
}

// fleetd run registers the outbox's housekeeping jobs: the first tick
// expires a command whose deadline came and prunes one closed more than
// 30 days ago.
func TestRunsTickExpiresAndPrunesCommands(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	first := h.start(t, h.options())
	res, _ := enrollOverRun(t, h, first)
	first.stop(t)
	id := res.GetMachineId().GetValue()
	start := time.Now()
	h.seedCommands(t, id, start.Add(-time.Minute), start.Add(-outbox.Retention-time.Hour))

	var now atomic.Int64
	now.Store(start.UnixNano())
	ticks := make(chan time.Time)
	opts := h.options()
	opts.Now = func() time.Time { return time.Unix(0, now.Load()) }
	opts.HousekeepingTicks = ticks
	h.start(t, opts)
	if st := h.commandState(t, "c-due"); st != "pending" {
		t.Fatalf("c-due is %q before any tick", st)
	}
	ticks <- time.Time{}
	ticks <- time.Time{} // the second send returns once the first check finished
	if st := h.commandState(t, "c-due"); st != "expired" {
		t.Errorf("c-due is %q after a tick, want expired", st)
	}
	if st := h.commandState(t, "c-old"); st != "" {
		t.Errorf("c-old is %q after a tick, want pruned", st)
	}
	if ev := h.logs.events(t, "outbox.expired"); len(ev) != 1 || ev[0]["command_id"] != "c-due" {
		t.Errorf("outbox.expired %v", ev)
	}
	if ev := h.logs.events(t, "outbox.pruned"); len(ev) != 1 || ev[0]["commands"] != float64(1) {
		t.Errorf("outbox.pruned %v", ev)
	}
}
