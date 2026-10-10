package ring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// 02i rule 1: seqs are reserved in blocks of 1024 before they are handed
// out, so a restart resumes above every seq handed out, never at one.
func TestSeqsAreReservedInBlocksAndResumeAboveTheMark(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	db, err := ring.Open(ctx, dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seqs, err := db.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for want := uint64(1); want <= 3; want++ {
		got, err := seqs.Next(ctx)
		if err != nil || got != want {
			t.Fatalf("Next = %d, %v; want %d", got, err, want)
		}
	}
	st, err := db.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Marks.SeqHWM != 1024 {
		t.Errorf("seq_hwm after handing out 1..3 = %d, want 1024 (one block)", st.Marks.SeqHWM)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	again := open(t, dir)
	seqs, err = again.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := seqs.Next(ctx); err != nil || got != 1025 {
		t.Errorf("first Next after a restart = %d, %v; want 1025 (above the stored mark)", got, err)
	}
	if st, _ := again.State(ctx); st.Marks.SeqHWM != 2048 {
		t.Errorf("seq_hwm after the restart's first seq = %d, want 2048", st.Marks.SeqHWM)
	}
}

// A floor is saved when it is taken, so a restart before any seq is handed
// out (a snapshot that failed) still resumes above the hub's seq and does not
// make the hub handle the same rollback again.
func TestAFloorSurvivesARestart(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	db, err := ring.Open(ctx, dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seqs, err := db.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := seqs.Floor(ctx, 5000); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again := open(t, dir)
	if st, err := again.State(ctx); err != nil || st.Marks.SeqHWM != 5000 {
		t.Errorf("seq_hwm after Floor(5000) and a restart = %d, %v; want 5000", st.Marks.SeqHWM, err)
	}
	seqs, err = again.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := seqs.Next(ctx); err != nil || got != 5001 {
		t.Errorf("first Next after a restart = %d, %v; want 5001", got, err)
	}
}

// 02i rule 2: after HelloAck the next seq is above hub_last_event_seq, and a
// lower floor changes nothing.
func TestFloorRaisesTheNextSeqAndNeverLowersIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name   string
		before int // seqs handed out before the floor
		floor  uint64
		want   uint64
	}{
		{"fresh agent, hub holds 5000 (a wiped state folder)", 0, 5000, 5001},
		{"hub behind the agent (a restored hub)", 10, 3, 11},
		{"hub at the last seq handed out", 10, 10, 11},
		{"zero floor", 2, 0, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := open(t, stateDir(t))
			seqs, err := db.Seqs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for range tc.before {
				if _, err := seqs.Next(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := seqs.Floor(ctx, tc.floor); err != nil {
				t.Fatal(err)
			}
			got, err := seqs.Next(ctx)
			if err != nil || got != tc.want {
				t.Errorf("Next after Floor(%d) = %d, %v; want %d", tc.floor, got, err, tc.want)
			}
			if st, _ := db.State(ctx); st.Marks.SeqHWM < got {
				t.Errorf("seq_hwm %d is below the seq %d handed out", st.Marks.SeqHWM, got)
			}
		})
	}
}

// Last is Heartbeat.event_seq: the highest seq this process handed out, never
// a reserved one or a floor that no Next has used yet.
func TestLastIsTheHighestSeqHandedOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := stateDir(t)
	db := open(t, dir)
	seqs, err := db.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs.Last(); got != 0 {
		t.Errorf("Last before any Next = %d, want 0", got)
	}
	for range 3 {
		if _, err := seqs.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := seqs.Last(); got != 3 {
		t.Errorf("Last after 1..3 = %d, want 3 (not the reserved 1024)", got)
	}
	if err := seqs.Floor(ctx, 5000); err != nil {
		t.Fatal(err)
	}
	if got := seqs.Last(); got != 3 {
		t.Errorf("Last after Floor(5000) = %d, want 3 until a seq is handed out", got)
	}
	if v, err := seqs.Next(ctx); err != nil || v != 5001 || seqs.Last() != 5001 {
		t.Errorf("Next after the floor = %d, %v; Last %d; want 5001 for both", v, err, seqs.Last())
	}
	again, err := db.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Last(); got != 0 {
		t.Errorf("Last of a new allocator = %d, want 0 until its first Next", got)
	}
}

// fleet-agent run tells a stop signal from a real failure with
// errors.Is(err, ctx.Err()) (cmd/fleet-agent/run.go), so a cancelled context
// must stay visible in the error Seqs returns.
func TestSeqsWithACancelledContextWrapsTheCancellation(t *testing.T) {
	t.Parallel()
	db, err := ring.Open(context.Background(), stateDir(t), ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.Seqs(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Seqs on a cancelled context = %v, want an error wrapping context.Canceled", err)
	}
}
