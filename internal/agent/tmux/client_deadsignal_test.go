package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// The list-panes line has eight fields, the sixth pane_dead_signal. tmux
// prints a signal by its sys_signame name where the OS has one (macOS:
// "kill") and by number elsewhere (Linux: "9"; tmux.c sig2name), and 3.2a,
// which has no pane_dead_signal, prints nothing. The client turns it into
// one closed set, never tmux's text. The signal is a diagnostic, not
// structure: text it cannot read is SignalOther with no number, and the pane
// still comes back.
func TestListPanesReadsTheDeadSignalIntoAClosedSet(t *testing.T) {
	usr1 := map[string]string{"darwin": "other:30", "linux": "other:10"}[runtime.GOOS]
	cases := []struct {
		name, line string
		dead       bool
		status     int
		signal     string
		bad        bool
	}{
		{"live pane", "$0 @0 %0 0   80 24", false, tmux.NoStatus, "", false},
		{"exited 4", "$0 @1 %1 1 4  80 24", true, 4, "", false},
		{"exited 0", "$0 @1 %1 1 0  80 24", true, 0, "", false},
		{"3.2a dead before its status", "$0 @1 %1 1   80 24", true, tmux.NoStatus, "", false},
		{"linux 1", "$0 @1 %1 1  1 80 24", true, tmux.NoStatus, tmux.SignalHup, false},
		{"linux 2", "$0 @1 %1 1  2 80 24", true, tmux.NoStatus, tmux.SignalInt, false},
		{"linux 3", "$0 @1 %1 1  3 80 24", true, tmux.NoStatus, tmux.SignalQuit, false},
		{"linux 6", "$0 @1 %1 1  6 80 24", true, tmux.NoStatus, tmux.SignalAbort, false},
		{"linux 9", "$0 @1 %1 1  9 80 24", true, tmux.NoStatus, tmux.SignalKill, false},
		{"linux 11", "$0 @1 %1 1  11 80 24", true, tmux.NoStatus, tmux.SignalSegv, false},
		{"linux 13", "$0 @1 %1 1  13 80 24", true, tmux.NoStatus, tmux.SignalPipe, false},
		{"linux 15", "$0 @1 %1 1  15 80 24", true, tmux.NoStatus, tmux.SignalTerm, false},
		{"macos hup", "$0 @1 %1 1  hup 80 24", true, tmux.NoStatus, tmux.SignalHup, false},
		{"macos int", "$0 @1 %1 1  int 80 24", true, tmux.NoStatus, tmux.SignalInt, false},
		{"macos quit", "$0 @1 %1 1  quit 80 24", true, tmux.NoStatus, tmux.SignalQuit, false},
		{"macos abrt", "$0 @1 %1 1  abrt 80 24", true, tmux.NoStatus, tmux.SignalAbort, false},
		{"macos kill", "$0 @1 %1 1  kill 80 24", true, tmux.NoStatus, tmux.SignalKill, false},
		{"macos segv", "$0 @1 %1 1  segv 80 24", true, tmux.NoStatus, tmux.SignalSegv, false},
		{"macos pipe", "$0 @1 %1 1  pipe 80 24", true, tmux.NoStatus, tmux.SignalPipe, false},
		{"macos term", "$0 @1 %1 1  term 80 24", true, tmux.NoStatus, tmux.SignalTerm, false},
		{"another number", "$0 @1 %1 1  31 80 24", true, tmux.NoStatus, "other:31", false},
		{"the highest number", "$0 @1 %1 1  127 80 24", true, tmux.NoStatus, "other:127", false},
		{"another name", "$0 @1 %1 1  usr1 80 24", true, tmux.NoStatus, usr1, false},
		{"a live pane drops a signal", "$0 @1 %1 0  9 80 24", false, tmux.NoStatus, "", false},
		{"zero", "$0 @1 %1 1  0 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"past 127", "$0 @1 %1 1  128 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"leading zero", "$0 @1 %1 1  09 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"signed", "$0 @1 %1 1  +9 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"upper case", "$0 @1 %1 1  KILL 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"sig prefix", "$0 @1 %1 1  sigkill 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"unknown name", "$0 @1 %1 1  bogus 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"punctuation", "$0 @1 %1 1  k-ll 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"too long", "$0 @1 %1 1  " + strings.Repeat("a", 17) + " 80 24", true, tmux.NoStatus, tmux.SignalOther, false},
		{"a live pane drops junk", "$0 @1 %1 0  bogus 80 24", false, tmux.NoStatus, "", false},
		{"both a status and a signal", "$0 @1 %1 1 4 9 80 24", false, 0, "", true},
		{"pane_dead not 0 or 1", "$0 @1 %1 2 4  80 24", false, 0, "", true},
		{"the old seven fields", "$0 @1 %1 1 4 80 24", false, 0, "", true},
		{"nine fields", "$0 @1 %1 1 4  80 24 x", false, 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog := writeFake(t, "3.7c", goodFlags)
			t.Setenv("FAKE_PANES", tc.line+`\n`)
			r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
			r.waitState(t, tmux.StateUp)
			panes, err := r.ListPanes(ctxT(t))
			if tc.bad {
				if !errors.Is(err, tmux.ErrBadReply) {
					t.Fatalf("ListPanes(%q) = %+v, %v; want ErrBadReply", tc.line, panes, err)
				}
				return
			}
			if err != nil || len(panes) != 1 {
				t.Fatalf("ListPanes(%q) = %+v, %v; want one pane", tc.line, panes, err)
			}
			p := panes[0]
			if p.Dead != tc.dead || p.DeadStatus != tc.status || p.DeadSignal != tc.signal || p.Width != 80 || p.Height != 24 {
				t.Fatalf("ListPanes(%q) = %+v, want Dead %v DeadStatus %d DeadSignal %q, 80x24", tc.line, p, tc.dead, tc.status, tc.signal)
			}
		})
	}
}

// A signal the client cannot read on one pane costs that pane its signal
// name, never the other panes: a pane kept by remain-on-exit stays dead, so
// failing the whole list would blind every later read of the server.
func TestListPanesKeepsOtherPanesPastABadSignal(t *testing.T) {
	prog := writeFake(t, "3.7c", goodFlags)
	t.Setenv("FAKE_PANES", `$0 @1 %1 1  iot 80 24\n$0 @0 %0 0   100 30\n`)
	r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
	r.waitState(t, tmux.StateUp)
	panes, err := r.ListPanes(ctxT(t))
	if err != nil || len(panes) != 2 {
		t.Fatalf("ListPanes = %+v, %v; want two panes", panes, err)
	}
	a, b := panes[0], panes[1]
	if a.ID.String() != "%1" || !a.Dead || a.DeadSignal != tmux.SignalOther {
		t.Fatalf("pane A = %+v, want %%1 dead with DeadSignal %q", a, tmux.SignalOther)
	}
	if b.ID.String() != "%0" || b.Dead || b.DeadSignal != "" || b.Width != 100 || b.Height != 30 {
		t.Fatalf("pane B = %+v, want %%0 live, no signal, 100x30", b)
	}
}

// The client asks for the eight fields in this order.
func TestListPanesAsksForTheDeadSignal(t *testing.T) {
	prog := writeFake(t, "3.7c", goodFlags)
	t.Setenv("FAKE_PANES", `$0 @0 %0 0   80 24\n`)
	r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
	r.waitState(t, tmux.StateUp)
	if _, err := r.ListPanes(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	cmds, err := os.ReadFile(filepath.Join(filepath.Dir(prog), "commands"))
	if err != nil {
		t.Fatal(err)
	}
	want := "list-panes -a -F '#{session_id} #{window_id} #{pane_id} #{pane_dead} #{pane_dead_status} #{pane_dead_signal} #{pane_width} #{pane_height}'\n"
	if !strings.Contains(string(cmds), want) {
		t.Fatalf("commands sent:\n%s\nwant the line %q", cmds, want)
	}
}
