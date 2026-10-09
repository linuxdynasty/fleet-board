package scrub

import "testing"

// No known input needs maxRounds to settle, so the branch for text that has
// not settled is reached with a bound of 1 and an input that needs 2 rounds:
// the first round exposes a password the second would remove.
func TestSettleFailsClosed(t *testing.T) {
	t.Parallel()
	const in = "a://cookie: \"-.0@x"
	cases := []struct {
		name   string
		rounds int
		limit  int
		want   string
	}{
		{"enough rounds", maxRounds, 64, "a://cookie:[redacted]@x"},
		{"bound reached", 1, 64, redacted},
		// The cut never splits a placeholder.
		{"bound reached, small cap", 1, 4, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opt := Options{MaxBytes: tc.limit}
			got := settle(in, opt, tc.rounds)
			if got != tc.want {
				t.Errorf("settle(%q, %d rounds)\n got %q\nwant %q", in, tc.rounds, got, tc.want)
			}
			if again := Text(got, opt); again != got {
				t.Errorf("second pass changed %q to %q", got, again)
			}
		})
	}
}
