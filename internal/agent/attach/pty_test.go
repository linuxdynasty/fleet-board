package attach

import (
	"slices"
	"testing"
)

// The attach client dies at once without a capable TERM, so OpenClient
// defaults a missing, empty, dumb or unknown TERM to xterm-256color and
// inherits anything else untouched.
func TestEnsureTerm(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  []string
		// want nil means "keep the environment as is".
		want []string
	}{
		{name: "missing", env: []string{"PATH=/bin"}, want: []string{"PATH=/bin", "TERM=xterm-256color"}},
		{name: "empty", env: []string{"TERM="}, want: []string{"TERM=xterm-256color"}},
		{name: "dumb", env: []string{"TERM=dumb"}, want: []string{"TERM=xterm-256color"}},
		{name: "unknown", env: []string{"TERM=unknown"}, want: []string{"TERM=xterm-256color"}},
		{name: "capable", env: []string{"TERM=tmux-256color"}, want: nil},
		{name: "keeps the rest", env: []string{"A=1", "TERM=dumb", "B=2"}, want: []string{"A=1", "B=2", "TERM=xterm-256color"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ensureTerm(tc.env); !slices.Equal(got, tc.want) {
				t.Fatalf("ensureTerm(%q) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}
