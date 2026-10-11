package ring_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Ruling fork 4 i: the log row and the command-seq advance are one store
// job, after the epoch check. A wrong epoch or a replayed seq writes
// nothing; a known command_id is answered from its entry before either check.
func TestLogAndApplyChecksEpochThenSeqAndWritesBothOrNeither(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	if err := db.StartEpoch(ctx, 5); err != nil {
		t.Fatal(err)
	}
	c := command("c-1", 5, 3)
	c.Body = []byte("body-1")
	e, dup, err := db.LogAndApply(ctx, c)
	if err != nil || dup {
		t.Fatalf("LogAndApply: dup %v, err %v; want a new entry", dup, err)
	}
	if e.Status != ring.StatusReceived || !bytes.Equal(e.Body, []byte("body-1")) {
		t.Errorf("entry %+v, want received with its body", e)
	}
	if st, _ := db.State(ctx); st.CommandSeq != 3 {
		t.Errorf("command seq %d, want 3", st.CommandSeq)
	}

	for _, tc := range []struct {
		name string
		c    ring.Command
		want error
	}{
		{"another epoch", command("c-2", 4, 9), ring.ErrWrongEpoch},
		{"a seq at the last", command("c-3", 5, 3), ring.ErrSeqReplayed},
		{"a seq below the last", command("c-4", 5, 1), ring.ErrSeqReplayed},
	} {
		if _, _, err := db.LogAndApply(ctx, tc.c); !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
		}
		if _, err := db.Command(ctx, tc.c.ID); !errors.Is(err, ring.ErrUnknownCommand) {
			t.Errorf("%s: the refused command was logged (err %v)", tc.name, err)
		}
	}
	if st, _ := db.State(ctx); st.CommandSeq != 3 {
		t.Errorf("after the refusals the command seq is %d, want 3", st.CommandSeq)
	}

	// A re-issue under a new epoch keeps its command_id: answered from the
	// log, never refused for its epoch or seq, and the seq is not moved.
	if err := db.StartEpoch(ctx, 6); err != nil {
		t.Fatal(err)
	}
	again, dup, err := db.LogAndApply(ctx, command("c-1", 6, 1))
	if err != nil || !dup || again.Epoch != 5 || again.Seq != 3 {
		t.Fatalf("re-issue: entry %+v dup %v err %v; want the logged entry", again, dup, err)
	}
	if st, _ := db.State(ctx); st.CommandSeq != 0 {
		t.Errorf("a known command_id moved the command seq to %d", st.CommandSeq)
	}
}

// A crash between the insert and the seq advance must leave neither: a seq
// applied with no log row would turn the hub's re-send into SEQ_REPLAYED on
// an unknown command_id, a lost command. The seeded failure is a trigger
// that aborts the seq update inside the job.
func TestLogAndApplyIsOneJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := stateDir(t)
	db := open(t, dir)
	if err := db.StartEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(t, dir).ExecContext(ctx, `CREATE TRIGGER seeded_crash BEFORE UPDATE OF last_applied_command_seq
		ON agent_state BEGIN SELECT RAISE(ABORT, 'seeded crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LogAndApply(ctx, command("c-1", 2, 1)); err == nil {
		t.Fatal("LogAndApply succeeded through the seeded crash")
	}
	if _, err := db.Command(ctx, "c-1"); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Errorf("the log row survived a failed seq advance (err %v)", err)
	}
}

// The executor marks an entry running as it starts it, and the resume pass
// reads every entry not finished, in the order they were applied.
func TestRunningAndOpenEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	if err := db.StartEpoch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"a", "b", "c"} {
		if _, _, err := db.LogAndApply(ctx, command(id, 1, uint64(i+1))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetRunning(ctx, "b", 2); err != nil {
		t.Fatalf("SetRunning: %v", err)
	}
	if err := db.SetRunning(ctx, "b", 1); err != nil {
		t.Fatalf("SetRunning a lower step: %v", err)
	}
	if err := db.RecordResult(ctx, "c", []byte("r"), t0); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunning(ctx, "c", 0); !errors.Is(err, ring.ErrResultLogged) {
		t.Errorf("SetRunning on a finished entry: %v, want ErrResultLogged", err)
	}
	if err := db.SetRunning(ctx, "zz", 0); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Errorf("SetRunning on an unknown entry: %v, want ErrUnknownCommand", err)
	}
	open, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].ID != "a" || open[0].Status != ring.StatusReceived ||
		open[1].ID != "b" || open[1].Status != ring.StatusRunning || open[1].Step != 2 {
		t.Errorf("open entries %+v, want a received, then b running at step 2", open)
	}
}
