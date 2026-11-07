package logstore

import "testing"

// TestWindowEndSaturatesOnOverflow pins the replay window bound: a
// start+maxBytes sum that wraps mod 2^64 must clamp to the end of the
// range instead of landing below start and skipping every row. No such
// stream can be ingested (the SQLite driver refuses offsets with the
// high bit set), so the bound is tested here rather than end to end.
func TestWindowEndSaturatesOnOverflow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		start    uint64
		maxBytes uint64
		want     uint64
	}{
		{"zero means to the end", 100, 0, ^uint64(0)},
		{"plain window", 100, 20, 120},
		{"exact top without wrap", ^uint64(0) - 20, 20, ^uint64(0)},
		{"wrap saturates", ^uint64(0) - 10, 20, ^uint64(0)},
		{"zero start with huge window", 0, ^uint64(0), ^uint64(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := windowEnd(tc.start, tc.maxBytes); got != tc.want {
				t.Errorf("windowEnd(%d, %d) = %d, want %d", tc.start, tc.maxBytes, got, tc.want)
			}
		})
	}
}
