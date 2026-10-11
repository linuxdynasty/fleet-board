package tmux_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// Whatever tmux prints, parsing a list-panes line never panics and a pane
// it returns carries a DeadSignal from the closed set: never tmux's text.
func FuzzParsePane(f *testing.F) {
	for _, s := range []string{
		"$0 @0 %0 0   80 24", "$0 @1 %1 1 4  80 24", "$0 @1 %1 1  9 80 24",
		"$0 @1 %1 1  kill 80 24", "$0 @1 %1 1  31 80 24", "$0 @1 %1 1  usr1 80 24",
		"$0 @1 %1 1  iot 80 24", "$0 @1 %1 1  128 80 24", "$0 @1 %1 1  +9 80 24",
		"$0 @1 %1 0  bogus 80 24", "$0 @1 %1 1 4 9 80 24", "$0 @1 %1 1 4 80 24",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		p, ok := tmux.ParsePaneForTest(line)
		if ok && !closedSignal(p.DeadSignal) {
			t.Fatalf("parsePane(%q).DeadSignal = %q, not in the closed set", line, p.DeadSignal)
		}
	})
}

func closedSignal(s string) bool {
	switch s {
	case "", tmux.SignalHup, tmux.SignalInt, tmux.SignalQuit, tmux.SignalAbort, tmux.SignalKill,
		tmux.SignalSegv, tmux.SignalPipe, tmux.SignalTerm, tmux.SignalOther:
		return true
	}
	n, ok := strings.CutPrefix(s, tmux.SignalOther+":")
	if !ok {
		return false
	}
	v, err := strconv.Atoi(n)
	return err == nil && v >= 1 && v <= 127 && strconv.Itoa(v) == n
}
