package inventory_test

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// typer is a person at the keyboard: a normal tmux client attached
// through a pseudo-terminal the test owns. Test files may start
// processes; the tmuxtest package may not.
type typer struct {
	cmd *exec.Cmd
	pty *os.File
}

func startTyper(t *testing.T, srv *tmuxtest.Server, session string) *typer {
	t.Helper()
	argv := srv.Argv("attach-session", "-t", session)
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // G204: the tmux under test and the rig's own arguments
	cmd.Env = srv.Env()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("attach through a pty: %v", err)
	}
	ty := &typer{cmd: cmd, pty: f}
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := f.Read(buf); err != nil { // drain the screen so tmux never blocks
				return
			}
		}
	}()
	t.Cleanup(func() {
		_, _ = f.WriteString("\x02d") // C-b d: detach
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = f.Close()
	})
	return ty
}

// key types one key and reports the time just before it landed.
func (y *typer) key(t *testing.T, s string) time.Time {
	t.Helper()
	t0 := time.Now()
	if _, err := y.pty.WriteString(s); err != nil {
		t.Fatalf("type %q: %v", s, err)
	}
	return t0
}

// paneInput waits for the PaneInput event for pane and reports it with
// the index after it.
func paneInput(t *testing.T, r *obsRig, from int, what, pane string) (*agentv1.Event, int) {
	t.Helper()
	return r.rec.waitFor(t, from, what, func(ev *agentv1.Event) bool {
		in := ev.GetPaneInput()
		return in != nil && in.GetPane().GetPaneId() == pane
	})
}

// inputReads is the rig's fleet_agent_tmux_input_reads_total for kind.
func inputReads(t *testing.T, r *obsRig, kind string) int64 {
	t.Helper()
	got := r.metric(t, "fleet_agent_tmux_input_reads_total", `server_kind="`+kind+`"`)
	if got == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(got), 10, 64)
	if err != nil {
		t.Fatalf("input reads = %q: %v", got, err)
	}
	return n
}

// paneIDs maps session tmux ids to their first pane's "%N" id.
func paneIDs(t *testing.T, r *obsRig) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, srv := range r.snapshot(t).GetTmuxServers() {
		for _, s := range srv.GetSessions() {
			for _, w := range s.GetWindows() {
				for _, p := range w.GetPanes() {
					if _, ok := out[s.GetTmuxId()]; !ok {
						out[s.GetTmuxId()] = p.GetKey().GetPaneId()
					}
				}
			}
		}
	}
	return out
}

// Proof 1: a key from an attached client produces PaneInput on the right
// pane within 2 s, in the session the control client is on and in another
// one. Proof 6 (second half): steady typing is at most one PaneInput per
// pane per 10 s. The Snapshot level carries the key time.
func TestPersonTypingCreditsTheRightPane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			dev.NewSession(t, "-s", "other", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			ids := paneIDs(t, r)
			work, other := ids["$0"], ids["$1"]
			if work == "" || other == "" {
				t.Fatalf("pane ids = %v, want $0 and $1", ids)
			}

			// The attaches may credit as input themselves (a person who
			// attached is at the keyboard); the 10 s per-pane gate runs
			// past before the keys below, so each key is due exactly one
			// new event whatever the attaches did.
			p1 := startTyper(t, dev, "work")
			p2 := startTyper(t, dev, "other")
			time.Sleep(11 * time.Second)
			base := len(r.rec.all())

			t0 := p1.key(t, "x")
			ev, _ := paneInput(t, r, base, "input on the work pane", work)
			if dt := time.Since(t0); dt > 2*time.Second {
				t.Fatalf("work PaneInput took %v, want within 2 s", dt)
			}
			in := ev.GetPaneInput()
			if in.GetPane().GetServer() != "default" || in.GetPane().GetPaneId() != work {
				t.Fatalf("PaneInput pane = %v, want default %s", in.GetPane(), work)
			}
			if in.GetGoal() != nil {
				t.Fatalf("PaneInput goal = %v, want unset: the registry is row 9's", in.GetGoal())
			}
			if in.GetInputTime() == nil || time.Since(in.GetInputTime().AsTime()) > 5*time.Second {
				t.Fatalf("PaneInput time = %v, want the key just typed", in.GetInputTime())
			}

			p2.key(t, "y")
			ev, _ = paneInput(t, r, base, "input on the other pane", other)
			if got := ev.GetPaneInput().GetPane().GetPaneId(); got != other {
				t.Fatalf("PaneInput pane = %s, want %s", got, other)
			}

			// Steady typing on work stays at one event per 10 s: the
			// key lands (the tripwire fires within a second) but no
			// second event follows inside the gate.
			countWork := func() int {
				n := 0
				for _, ev := range r.rec.all() {
					if in := ev.GetPaneInput(); in != nil && in.GetPane().GetPaneId() == work {
						n++
					}
				}
				return n
			}
			n0 := countWork()
			p1.key(t, "z")
			time.Sleep(4 * time.Second)
			if n := countWork(); n != n0 {
				t.Fatalf("%d work PaneInputs, want %d: at most one per 10 s", n, n0)
			}
			snap := r.snapshot(t)
			var level *agentv1.TmuxPane
			for _, srv := range snap.GetTmuxServers() {
				for _, s := range srv.GetSessions() {
					for _, w := range s.GetWindows() {
						for _, p := range w.GetPanes() {
							if p.GetKey().GetPaneId() == work {
								level = p
							}
						}
					}
				}
			}
			if level.GetLastInputTime() == nil {
				t.Fatal("the work pane has no last_input_time")
			}
		})
	}
}

// Proof 2: the agent's own actions never count as typing. Each of these
// moves no client's key time (measured on 3.2a and 3.7c): send-keys -l and
// named keys, new-session, new-window, select-window, select-pane,
// capture-pane, respawn-pane and resize, and the control client's own
// attach and reattach. No person is attached: only the control client is
// ever listed, so any PaneInput is a bug.
func TestAgentActionsCreditNoInput(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)

			none := func(what string) {
				t.Helper()
				for _, ev := range r.rec.all() {
					if ev.GetPaneInput() != nil {
						t.Fatalf("%s: PaneInput %+v, want none", what, ev.GetPaneInput())
					}
				}
			}
			dev.Run(t, "split-window", "-t", "work", "exec cat")
			for _, keys := range [][]string{{"-l", "hi"}, {"Enter"}, {"C-c"}, {"Escape"}, {"Up"}} {
				dev.Run(t, append([]string{"send-keys", "-t", "work:0.0"}, keys...)...)
			}
			dev.Run(t, "select-pane", "-t", "%1")
			dev.Run(t, "select-pane", "-t", "work:0.0")
			dev.Run(t, "select-window", "-t", "work:0")
			dev.Run(t, "new-window", "-t", "work", "exec cat")
			dev.Run(t, "capture-pane", "-p", "-t", "work:0.0")
			dev.Run(t, "resize-pane", "-t", "work:0.0", "-x", "90")
			dev.Run(t, "respawn-pane", "-t", "work:0.0", "-k", "exec cat")
			time.Sleep(3500 * time.Millisecond) // past tmux's 1 s check, whatever moved
			none("agent keys, selects, capture, resize and respawn")

			// new-session moves the tripwire (read from the 3.2a source):
			// wait until the client read it reports, then assert the read
			// credited nothing.
			before := inputReads(t, r, "default")
			dev.NewSession(t, "-s", "extra", "exec cat")
			deadline := time.Now().Add(wait)
			for inputReads(t, r, "default") == before && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if inputReads(t, r, "default") == before {
				t.Fatal("no client read followed the new session")
			}
			none("new-session")

			// The control client's own reattach: killing its session ends
			// the attach with %exit and the client lands on the lowest
			// session left. Its switch moves the tripwire; control clients
			// are ignored, so nothing is credited. The wait is for the
			// landed session, not for the drop in between: the drop is not
			// guaranteed to show between two 20 ms polls on a loaded
			// machine (Linux CI and a loaded Linux rig failed the drop
			// wait with the client still up), while the landed session is
			// the whole assertion.
			dev.Run(t, "kill-session", "-t", "work")
			waitSession(t, r, 1)
			time.Sleep(3500 * time.Millisecond)
			none("reattach")
			if got := r.o.Client("default").Session(); got != 1 {
				t.Fatalf("re-attached to %s, want $1 (extra)", got)
			}
		})
	}
}
