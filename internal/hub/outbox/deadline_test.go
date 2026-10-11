package outbox_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// The 02-contracts "Deadlines" table, one kind of each class.
func TestDefaultDeadlineFollowsTheContractTable(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]time.Duration{
		"send_keys": 2 * time.Minute, "interrupt": 2 * time.Minute, "run_harness_command": 2 * time.Minute,
		"capture_pane": 2 * time.Minute, "open_attach": 2 * time.Minute,
		"start_goal": 30 * time.Minute, "ensure_worktree": 30 * time.Minute, "prune_worktree": 30 * time.Minute,
		"ensure_goal_hooks": 30 * time.Minute,
		"stop_probe":        24 * time.Hour,
		"ensure_probe":      10 * time.Minute, "drain": 10 * time.Minute, "kill_window": 10 * time.Minute,
		"register_goal": 10 * time.Minute,
	} {
		if got := outbox.DefaultDeadline(kind); got != want {
			t.Errorf("DefaultDeadline(%q) = %v, want %v", kind, got, want)
		}
	}
}

// No command leaves the outbox without a deadline (6e fork 11: the agent
// treats an unset not_after_time as already past). Every kind arm of the
// Command, read from its descriptor so a new arm cannot slip by, enqueued
// with none gets now plus its class default, both in the row and in the
// stored Command the agent receives.
func TestEnqueueFillsTheDefaultDeadlineForEveryKind(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	arms := (&agentv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	if arms.Len() == 0 {
		t.Fatal("no kind arms")
	}
	for i := range arms.Len() {
		f := arms.Get(i)
		c := &agentv1.Command{CommandId: &typev1.CommandId{Value: "c-" + string(f.Name())}}
		r := c.ProtoReflect()
		r.Set(f, protoreflect.ValueOfMessage(r.NewField(f).Message()))
		op, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: c, Now: t0})
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		want := t0.Add(outbox.DefaultDeadline(string(f.Name())))
		if got := time.UnixMilli(int64(count(t, s, `SELECT not_after_time FROM commands WHERE command_id = ?`,
			op.CommandID))); !got.Equal(want) {
			t.Errorf("%s: not_after_time %v, want %v", f.Name(), got, want)
		}
		row, _ := readRow(t, s, op.CommandID)
		var stored agentv1.Command
		if err := proto.Unmarshal(row.command, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.GetNotAfterTime() == nil || !stored.GetNotAfterTime().AsTime().Equal(want) {
			t.Errorf("%s: the stored Command's not_after_time %v, want %v", f.Name(), stored.GetNotAfterTime(), want)
		}
	}
}

// A deadline the caller set is kept as it is.
func TestEnqueueKeepsTheCallersDeadline(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mustEnqueue(t, s, "m1", "c1") // interrupt: due t0 + 1 h, not t0 + 2 min
	if got := time.UnixMilli(int64(count(t, s, `SELECT not_after_time FROM commands`))); !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("not_after_time %v, want the caller's %v", got, t0.Add(time.Hour))
	}
}
