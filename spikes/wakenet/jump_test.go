package main

import (
	"testing"
	"time"
)

func TestJumpDetector(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	type obs struct {
		wall time.Duration // offset of the wall reading from base
		mono time.Duration // monotonic elapsed since start
	}
	cases := []struct {
		name     string
		obs      []obs
		wantJump bool
		wantGap  time.Duration
	}{
		{
			name: "first reading never jumps",
			obs:  []obs{{0, 0}},
		},
		{
			name: "steady ticks with jitter under threshold",
			obs:  []obs{{0, 0}, {5 * time.Second, 5 * time.Second}, {10*time.Second + 300*time.Millisecond, 10 * time.Second}},
		},
		{
			name:     "ten minute sleep between ticks",
			obs:      []obs{{0, 0}, {5 * time.Second, 5 * time.Second}, {10*time.Minute + 10*time.Second, 10 * time.Second}},
			wantJump: true,
			wantGap:  10 * time.Minute,
		},
		{
			name: "gap equal to threshold is not a jump",
			obs:  []obs{{0, 0}, {7 * time.Second, 5 * time.Second}},
		},
		{
			name:     "wall clock stepped back",
			obs:      []obs{{0, 0}, {-55 * time.Second, 5 * time.Second}},
			wantJump: true,
			wantGap:  -time.Minute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := JumpDetector{Threshold: 2 * time.Second}
			var got Jump
			var jumped bool
			for _, o := range tc.obs {
				got, jumped = d.Observe(base.Add(o.wall), o.mono)
			}
			if jumped != tc.wantJump {
				t.Fatalf("jumped = %v, want %v (jump %+v)", jumped, tc.wantJump, got)
			}
			if jumped && got.Gap != tc.wantGap {
				t.Fatalf("gap = %v, want %v", got.Gap, tc.wantGap)
			}
		})
	}
}
