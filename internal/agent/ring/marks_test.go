package ring_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// 02i rule 1: seq_hwm and the EventCounters never decrease; a lower value is
// ignored field by field and the stored one is returned.
func TestMarksNeverGoBackwards(t *testing.T) {
	t.Parallel()
	type step struct {
		raise ring.Marks
		want  ring.Marks
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"first block", []step{
			{ring.Marks{SeqHWM: 1023}, ring.Marks{SeqHWM: 1023}},
		}},
		{"next block", []step{
			{ring.Marks{SeqHWM: 1023}, ring.Marks{SeqHWM: 1023}},
			{ring.Marks{SeqHWM: 2047}, ring.Marks{SeqHWM: 2047}},
		}},
		{"a lower seq_hwm is ignored", []step{
			{ring.Marks{SeqHWM: 2047}, ring.Marks{SeqHWM: 2047}},
			{ring.Marks{SeqHWM: 1023}, ring.Marks{SeqHWM: 2047}},
			{ring.Marks{}, ring.Marks{SeqHWM: 2047}},
		}},
		{"each field on its own", []step{
			{
				ring.Marks{SeqHWM: 4095, Shed: 7, Coalesced: 3, Evicted: 1},
				ring.Marks{SeqHWM: 4095, Shed: 7, Coalesced: 3, Evicted: 1},
			},
			{
				ring.Marks{SeqHWM: 1023, Shed: 9, Coalesced: 2, Evicted: 0},
				ring.Marks{SeqHWM: 4095, Shed: 9, Coalesced: 3, Evicted: 1},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := stateDir(t)
			db := open(t, dir)
			for i, s := range tc.steps {
				got, err := db.RaiseMarks(context.Background(), s.raise)
				if err != nil {
					t.Fatalf("step %d: RaiseMarks: %v", i, err)
				}
				if got != s.want {
					t.Errorf("step %d: RaiseMarks(%+v) = %+v, want %+v", i, s.raise, got, s.want)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := open(t, dir).State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.steps[len(tc.steps)-1].want; st.Marks != want {
				t.Errorf("after a reopen marks are %+v, want %+v", st.Marks, want)
			}
		})
	}
}

// The file itself refuses a lower mark, a lower epoch or a new install_id,
// whoever writes it: the trigger backs the rule up.
func TestTheFileRefusesAStepBack(t *testing.T) {
	t.Parallel()
	const back = "never goes backwards"
	cases := []struct{ name, sql, why string }{
		{"seq_hwm", `UPDATE agent_state SET seq_hwm = 1`, back},
		{"shed_total", `UPDATE agent_state SET shed_total = 0`, back},
		{"coalesced_total", `UPDATE agent_state SET coalesced_total = 0`, back},
		{"evicted_total", `UPDATE agent_state SET evicted_total = 0`, back},
		{"last_epoch", `UPDATE agent_state SET last_epoch = 1`, back},
		{"command seq in the same epoch", `UPDATE agent_state SET last_applied_command_seq = 1`, back},
		{"install_id", `UPDATE agent_state SET install_id = 'another'`, back},
		{"a second row", `INSERT INTO agent_state(id, install_id, created_time) VALUES (2, 'x', 0)`, "CHECK constraint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := stateDir(t)
			db := open(t, dir)
			ctx := context.Background()
			if _, err := db.RaiseMarks(ctx, ring.Marks{SeqHWM: 2047, Shed: 2, Coalesced: 2, Evicted: 2}); err != nil {
				t.Fatal(err)
			}
			if err := db.StartEpoch(ctx, 5); err != nil {
				t.Fatal(err)
			}
			if err := db.ApplyCommandSeq(ctx, 5, 9); err != nil {
				t.Fatal(err)
			}
			if _, err := raw(t, dir).ExecContext(ctx, tc.sql); err == nil || !strings.Contains(err.Error(), tc.why) {
				t.Errorf("%s: %q gave %v, want a refusal naming %q", tc.name, tc.sql, err, tc.why)
			}
		})
	}
}

// 02i "Connection epoch": the epoch only rises; a new epoch starts its
// command seq from zero, and a seq at or below the last applied is a replay.
func TestEpochAndCommandSeq(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db := open(t, dir)
	ctx := context.Background()
	steps := []struct {
		name string
		do   func() error
		want error
	}{
		{"seq before any epoch", func() error { return db.ApplyCommandSeq(ctx, 1, 1) }, ring.ErrWrongEpoch},
		{"epoch 0 is not above 0", func() error { return db.StartEpoch(ctx, 0) }, ring.ErrEpochNotAbove},
		{"epoch 7", func() error { return db.StartEpoch(ctx, 7) }, nil},
		{"seq 1", func() error { return db.ApplyCommandSeq(ctx, 7, 1) }, nil},
		{"seq 41", func() error { return db.ApplyCommandSeq(ctx, 7, 41) }, nil},
		{"seq 41 again", func() error { return db.ApplyCommandSeq(ctx, 7, 41) }, ring.ErrSeqReplayed},
		{"seq 40", func() error { return db.ApplyCommandSeq(ctx, 7, 40) }, ring.ErrSeqReplayed},
		{"an older epoch's seq", func() error { return db.ApplyCommandSeq(ctx, 6, 50) }, ring.ErrWrongEpoch},
		{"epoch 7 again", func() error { return db.StartEpoch(ctx, 7) }, ring.ErrEpochNotAbove},
		{"epoch 6", func() error { return db.StartEpoch(ctx, 6) }, ring.ErrEpochNotAbove},
		{"epoch 8", func() error { return db.StartEpoch(ctx, 8) }, nil},
		{"seq 1 in epoch 8", func() error { return db.ApplyCommandSeq(ctx, 8, 1) }, nil},
	}
	for _, s := range steps {
		if err := s.do(); !errors.Is(err, s.want) || (s.want == nil && err != nil) {
			t.Errorf("%s: %v, want %v", s.name, err, s.want)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := open(t, dir).State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != 8 || st.CommandSeq != 1 {
		t.Errorf("after a reopen epoch %d, command seq %d; want 8 and 1 (Hello.last_epoch, last_applied_command_seq)", st.Epoch, st.CommandSeq)
	}
}
