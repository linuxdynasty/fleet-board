package ring_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// A HelloAck epoch or hub_last_event_seq too large to leave room for the
// next epoch, or for one more block of seqs, is refused before anything is
// saved: agent.db never goes backwards, so a saved one would stop every
// later epoch or seq for good.
func TestAHubValueTooLargeIsRefusedAndSavesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name         string
		epoch, floor uint64
		refused      bool
	}{
		{"the largest epoch", ring.MaxEpoch, 0, false},
		{"the largest floor", 1, ring.MaxHubLastSeq, false},
		{"an epoch one above", ring.MaxEpoch + 1, 0, true},
		{"a floor one above", 1, ring.MaxHubLastSeq + 1, true},
		{"MaxInt64 epoch", math.MaxInt64, 0, true},
		{"MaxInt64 - 10 floor", 1, math.MaxInt64 - 10, true},
		{"MaxUint64 floor", 1, math.MaxUint64, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := open(t, stateDir(t))
			err := ring.CheckHubMarks(tc.epoch, tc.floor)
			if got := errors.Is(err, ring.ErrOutOfRange); got != tc.refused {
				t.Fatalf("CheckHubMarks(%d, %d) = %v, want refused %v", tc.epoch, tc.floor, err, tc.refused)
			}
			seqs, err := db.Seqs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			eerr, ferr := db.StartEpoch(ctx, tc.epoch), seqs.Floor(ctx, tc.floor)
			if tc.refused != (errors.Is(eerr, ring.ErrOutOfRange) || errors.Is(ferr, ring.ErrOutOfRange)) {
				t.Fatalf("StartEpoch = %v, Floor = %v; want refused %v", eerr, ferr, tc.refused)
			}
			if !tc.refused {
				// The room left is enough for the next seq block.
				if eerr != nil || ferr != nil {
					t.Fatalf("StartEpoch = %v, Floor = %v", eerr, ferr)
				}
				if v, err := seqs.Next(ctx); err != nil || v != tc.floor+1 {
					t.Errorf("Next after Floor(%d) = %d, %v; want %d", tc.floor, v, err, tc.floor+1)
				}
				return
			}
			st, err := db.State(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.epoch > ring.MaxEpoch && st.Epoch != 0 {
				t.Errorf("last_epoch = %d after a refused epoch, want 0", st.Epoch)
			}
			if tc.floor > ring.MaxHubLastSeq && st.Marks.SeqHWM != 0 {
				t.Errorf("seq_hwm = %d after a refused floor, want 0", st.Marks.SeqHWM)
			}
			if tc.floor > ring.MaxHubLastSeq {
				if v, err := seqs.Next(ctx); err != nil || v != 1 {
					t.Errorf("Next after a refused floor = %d, %v; want 1", v, err)
				}
			}
			if tc.epoch > ring.MaxEpoch {
				if err := db.StartEpoch(ctx, 1); err != nil {
					t.Errorf("a sane epoch after a refused one: %v", err)
				}
			}
		})
	}
}
