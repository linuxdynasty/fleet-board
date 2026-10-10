package scrub

import (
	"strings"
	"testing"
)

// Glued keys of different kinds: each key's \b fails until its left
// neighbour is a placeholder, so each round removes one more. Three keys
// need three changing rounds and a fourth to confirm.
var (
	awsKey    = "AKIA" + strings.Repeat("Z", 16)
	googleKey = "AIza" + strings.Repeat("x", 35)
	threeKeys = awsKey + googleKey + awsKey
)

// The bound is pinned from both sides: with maxRounds an input needing
// three changing rounds comes back scrubbed, and with one round fewer it
// fails closed. A bound of 1 reaches the fallback with a two-round input.
func TestSettleRoundBound(t *testing.T) {
	t.Parallel()
	const twoRounds = "a://cookie: \"-.0@x"
	cases := []struct {
		name          string
		in            string
		rounds, limit int
		want          Result
	}{
		{"two rounds, enough", twoRounds, maxRounds, 64, Result{Text: "a://cookie:[redacted]@x", Changed: true}},
		{"two rounds, bound 1", twoRounds, 1, 64, Result{Text: redacted, Changed: true, Unsettled: true}},
		// The cut never splits a placeholder.
		{"bound 1, small cap", twoRounds, 1, 4, Result{Text: "", Changed: true, Cut: true, Unsettled: true}},
		{"three rounds, maxRounds", threeKeys, maxRounds, 256, Result{Text: "AKIA[redacted]AIza[redacted]AKIA[redacted]", Changed: true}},
		{"three rounds, one fewer", threeKeys, maxRounds - 1, 256, Result{Text: redacted, Changed: true, Unsettled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opt := Options{MaxBytes: tc.limit}
			got := settle(tc.in, opt, tc.rounds)
			if got != tc.want {
				t.Errorf("settle(%q, %d rounds)\n got %+v\nwant %+v", tc.in, tc.rounds, got, tc.want)
			}
			if again := Text(got.Text, opt); again != got.Text {
				t.Errorf("second pass changed %q to %q", got.Text, again)
			}
		})
	}
}
