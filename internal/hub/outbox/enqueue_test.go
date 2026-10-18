package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// Enqueue, in the caller's writer job, stores one pending row: Operation
// PENDING, version 1, a hub_seq stamp, never sent, and the Command as given
// with epoch and seq unset (they are set per connection epoch at send).
func TestEnqueueStoresOnePendingRow(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	op := mustEnqueue(t, s, "m1", "c1")
	if !op.Created || op.CommandID != "c1" || op.MachineID != "m1" || op.Kind != "interrupt" ||
		op.State != "pending" || op.OpState != "PENDING" || op.Version != 1 || op.HubSeq <= 0 {
		t.Errorf("Op = %+v", op)
	}
	r, ok := readRow(t, s, "c1")
	if !ok {
		t.Fatal("no row for c1")
	}
	if r.machine != "m1" || r.kind != "interrupt" || r.state != "pending" || r.opState != "PENDING" || r.version != 1 ||
		r.hubSeq != op.HubSeq || r.sentEpoch.Valid || r.sentSeq.Valid || r.ackTime.Valid || r.doneTime.Valid {
		t.Errorf("row = %+v", r)
	}
	var stored agentv1.Command
	if err := proto.Unmarshal(r.command, &stored); err != nil {
		t.Fatal(err)
	}
	want := interrupt("c1")
	if !proto.Equal(&stored, want) {
		t.Errorf("stored command %v, want %v", &stored, want)
	}
}

func TestEnqueueCheckpointFollowsValidationAndPrecedesWrite(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	denied := errors.New("quota exhausted")
	checks := 0
	check := func() error { checks++; return denied }
	if _, err := enqueue(t, s, outbox.In{MachineID: "m9", Command: interrupt("c1"), Now: t0, Checkpoint: check}); !errors.Is(err, outbox.ErrMachineUnknown) {
		t.Fatalf("missing machine = %v", err)
	}
	if checks != 0 {
		t.Fatalf("invalid command called checkpoint %d times", checks)
	}
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: interrupt("c1"), Now: t0, Checkpoint: check}); !errors.Is(err, denied) {
		t.Fatalf("valid command = %v, want checkpoint error", err)
	}
	if n := count(t, s, `SELECT count(*) FROM commands`); n != 0 {
		t.Fatalf("denied command wrote %d rows", n)
	}
	mustEnqueue(t, s, "m1", "c1")
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: interrupt("c1"), Now: t0, Checkpoint: check}); !errors.Is(err, denied) {
		t.Errorf("valid stored command = %v, want checkpoint error", err)
	}
	if checks != 2 {
		t.Errorf("checkpoint calls = %d, want 2", checks)
	}
}

// A command_id already stored is a no-op that returns the stored row: no
// second row, no new hub_seq, nothing changed, even with another body.
func TestEnqueueIsIdempotentOnCommandID(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	first := mustEnqueue(t, s, "m1", "c1")
	other := interrupt("c1")
	other.GetInterrupt().GetPane().Server = "fleet"
	again, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: other, Now: t0.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.HubSeq != first.HubSeq || again.Version != 1 || again.CommandID != "c1" {
		t.Errorf("duplicate Enqueue = %+v, want the stored row %+v", again, first)
	}
	if n := count(t, s, `SELECT count(*) FROM commands`); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
	if n := count(t, s, `SELECT hub_seq FROM hub_counters`); int64(n) != first.HubSeq {
		t.Errorf("hub_seq %d, want %d: a duplicate takes no stamp", n, first.HubSeq)
	}
	r, _ := readRow(t, s, "c1")
	var stored agentv1.Command
	if err := proto.Unmarshal(r.command, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.GetInterrupt().GetPane().GetServer() != "default" {
		t.Error("the duplicate replaced the stored command")
	}
}

// A command without an id gets one minted: distinct, printable, stored.
func TestEnqueueMintsACommandID(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	a := mustEnqueue(t, s, "m1", "")
	b := mustEnqueue(t, s, "m1", "")
	if a.CommandID == "" || a.CommandID == b.CommandID || len(a.CommandID) > 64 {
		t.Errorf("minted ids %q and %q", a.CommandID, b.CommandID)
	}
	if _, ok := readRow(t, s, a.CommandID); !ok {
		t.Errorf("no row for the minted id %q", a.CommandID)
	}
}

// Enqueue runs in the caller's transaction: when the caller's job fails
// after it, the row goes with the job.
func TestEnqueueRollsBackWithTheCallersJob(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	boom := errors.New("the decision failed")
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := outbox.Enqueue(context.Background(), tx, outbox.In{MachineID: "m1", Command: interrupt("c1"), Now: t0}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write = %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM commands`); n != 0 {
		t.Errorf("%d rows after the caller's job failed, want 0", n)
	}
}

// What Enqueue refuses writes nothing.
func TestEnqueueRefusesAndWritesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		machine string
		cmd     func() *agentv1.Command
		now     time.Time
		want    error
	}{
		{"unknown machine", "m9", func() *agentv1.Command { return interrupt("c1") }, t0, outbox.ErrMachineUnknown},
		{"no machine", "", func() *agentv1.Command { return interrupt("c1") }, t0, outbox.ErrInvalid},
		{"no command", "m1", func() *agentv1.Command { return nil }, t0, outbox.ErrInvalid},
		{"no kind", "m1", func() *agentv1.Command { c := interrupt("c1"); c.Kind = nil; return c }, t0, outbox.ErrInvalid},
		{"deadline passed", "m1", func() *agentv1.Command { return interrupt("c1") }, t0.Add(time.Hour), outbox.ErrInvalid},
		{"no clock", "m1", func() *agentv1.Command { return interrupt("c1") }, time.Time{}, outbox.ErrInvalid},
		{"epoch set", "m1", func() *agentv1.Command { c := interrupt("c1"); c.Epoch = 3; return c }, t0, outbox.ErrInvalid},
		{"seq set", "m1", func() *agentv1.Command { c := interrupt("c1"); c.Seq = 1; return c }, t0, outbox.ErrInvalid},
		{"id too long", "m1", func() *agentv1.Command { return interrupt(strings.Repeat("a", 65)) }, t0, outbox.ErrInvalid},
		{"id with a space", "m1", func() *agentv1.Command { return interrupt("c 1") }, t0, outbox.ErrInvalid},
		{"id not ascii", "m1", func() *agentv1.Command { return interrupt("cé") }, t0, outbox.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			_, err := enqueue(t, s, outbox.In{MachineID: tc.machine, Command: tc.cmd(), Now: tc.now})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Enqueue = %v, want %v", err, tc.want)
			}
			if n := count(t, s, `SELECT count(*) FROM commands`); n != 0 {
				t.Errorf("%d rows, want 0", n)
			}
		})
	}
}

// The caller's Command is not changed by Enqueue, minting included.
func TestEnqueueLeavesTheCallersCommandAlone(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	c := interrupt("")
	c.NotAfterTime = timestamppb.New(t0.Add(time.Hour))
	before := proto.Clone(c)
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: c, Now: t0}); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(c, before) {
		t.Errorf("Enqueue changed the caller's command: %v", c)
	}
}

// A known command_id for another machine is a conflict, not the stored
// row; a deadline less than a millisecond ahead (the stored unit) is
// invalid, not an insert error.
func TestEnqueueRefusesAConflictAndASubMillisecondDeadline(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mustEnqueue(t, s, "m1", "c1")
	if _, err := enqueue(t, s, outbox.In{MachineID: "m2", Command: interrupt("c1"), Now: t0}); !errors.Is(err, outbox.ErrConflict) {
		t.Errorf("Enqueue for another machine = %v, want ErrConflict", err)
	}
	c := interrupt("c2")
	c.NotAfterTime = timestamppb.New(t0.Add(500 * time.Microsecond))
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: c, Now: t0}); !errors.Is(err, outbox.ErrInvalid) {
		t.Errorf("Enqueue with a deadline 0.5 ms ahead = %v, want ErrInvalid", err)
	}
	if n := count(t, s, `SELECT count(*) FROM commands`); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
}
