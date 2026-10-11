package tmux_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func TestKilledServerIsAbsentUntilWokenThenANewServerIsAttached(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			oldID := r.ServerStartID()
			oldPane := firstPane(t, r, 0)
			oldLine := literal(t, r.paneKey(oldPane.ID), "stale")

			srv.Kill(t)
			r.waitState(t, tmux.StateAbsent)
			if r.Up() || tmux.ControlDown(r.Client) != 1 {
				t.Fatalf("Up %v, ControlDown %d after the server died", r.Up(), tmux.ControlDown(r.Client))
			}
			if got := series(scrape(t, r.tel), "fleet_agent_tmux_control_up", `server_kind="fleet"`); got != "0" {
				t.Fatalf("fleet_agent_tmux_control_up = %q, want 0", got)
			}
			// The attach ended (tmux.exit eof or exit), then the server was
			// found absent (tmux.exit absent, once).
			reasons := exitReasons(t, r.logs)
			if len(reasons) != 2 || (reasons[0] != "eof" && reasons[0] != "exit") || reasons[1] != "absent" {
				t.Fatalf("tmux.exit reasons = %q, want [eof|exit absent]", reasons)
			}
			if _, err := r.ListPanes(ctxT(t)); !errors.Is(err, tmux.ErrNotAttached) {
				t.Fatalf("ListPanes while absent = %v, want ErrNotAttached", err)
			}
			// A wake with still no server tries once more and logs nothing new:
			// absent is logged once per absence.
			tries := tmux.AttemptsForTest(r.Client)
			r.Wake()
			waitTrue(t, "a second try while absent", func() bool {
				return tmux.AttemptsForTest(r.Client) > tries && r.State() == tmux.StateAbsent
			})
			if got := exitReasons(t, r.logs); len(got) != 2 {
				t.Fatalf("tmux.exit reasons after a wake while absent = %q, want 2", got)
			}

			// A new server on the same socket: 7d's socket watch wakes the
			// client; there is no timer.
			srv.NewSession(t, "-s", "again", "exec cat")
			r.Wake()
			r.waitState(t, tmux.StateUp)
			if r.ServerStartID() == oldID {
				t.Fatalf("server start id %q did not change with a new server", oldID)
			}
			// The new server reuses the pane id; a line built for the old start
			// is refused.
			if err := r.Send(ctxT(t), oldLine); !errors.Is(err, tmux.ErrWrongServer) {
				t.Fatalf("Send(line for the old server) = %v, want ErrWrongServer", err)
			}
		})
	}
}

func TestExitReattachesToTheLowestSessionAndResubscribes(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "a", "exec cat")
			srv.NewSession(t, "-s", "b", "exec cat")
			srv.NewSession(t, "-s", "c", "exec cat")
			n := newNotes()
			r := startClient(t, srv, n)
			r.waitState(t, tmux.StateUp)
			if s := r.Session(); s != 0 {
				t.Fatalf("attached to %s, want the lowest session $0", s)
			}
			id := r.ServerStartID()
			if err := r.Subscribe(ctxT(t), tmux.Subscription{Name: "sess-act", Format: "#{S:#{session_id}=#{session_activity} }"}); err != nil {
				t.Fatal(err)
			}
			_, next := n.waitFor(t, 0, "the first subscription value", isSub("sess-act"))

			srv.Run(t, "kill-session", "-t", "a")
			n.waitFor(t, next, "%exit", func(m tmux.Message) bool { _, ok := m.(tmux.Exit); return ok })
			waitEventCount(t, r.logs, "tmux.attached", 2)
			if s := r.Session(); s != 1 {
				t.Fatalf("re-attached to %s, want the then-lowest session $1", s)
			}
			if r.ServerStartID() != id {
				t.Fatal("the server start id changed though the server did not")
			}
			n.waitFor(t, next, "the subscription value after re-attach", isSub("sess-act"))
			if reasons := exitReasons(t, r.logs); len(reasons) != 1 || reasons[0] != "exit" {
				t.Fatalf("tmux.exit reasons = %q, want [exit]", reasons)
			}
			if strings.Contains(r.logs.String(), `"a"`) {
				t.Fatal("a session name reached a log line")
			}
		})
	}
}

func isSub(name string) func(tmux.Message) bool {
	return func(m tmux.Message) bool {
		s, ok := m.(tmux.SubscriptionChanged)
		return ok && s.Name == name
	}
}

// A person attached at 100x30 (a 100x29 window under the status line) keeps
// that size when the control client attaches: ignore-size, set at attach
// (spike 0.5 finding 9).
func TestIgnoreSizeKeepsThePersonsWindowSize(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-option", "-g", "window-size", "smallest")
			person := startPerson(t, srv, "work", 100, 30)
			defer person.close()
			waitSize(t, srv, "100x29")

			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			// The control client's own size (80x24 by default) must not count.
			p := firstPane(t, r, 0)
			if p.Width != 100 || p.Height != 29 {
				t.Fatalf("pane is %dx%d with the control client attached, want 100x29", p.Width, p.Height)
			}
		})
	}
}

func waitSize(t *testing.T, srv *tmuxtest.Server, want string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		got := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", "work:", "#{window_width}x#{window_height}"))
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("window is %s, want %s", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// person is a normal tmux client on a pseudo-terminal the test owns: a
// person at the keyboard (spikes/tmuxtyping/typer.go). Test files may start
// processes; the tmuxtest package may not.
type person struct {
	cmd  *exec.Cmd
	pty  *os.File
	done chan struct{}
}

func startPerson(t *testing.T, srv *tmuxtest.Server, session string, cols, rows uint16) *person {
	t.Helper()
	argv := srv.Argv("attach-session", "-t", session)
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // G204: the tmux under test and the rig's own arguments
	cmd.Env = srv.Env()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		t.Fatalf("attach through a pty: %v", err)
	}
	p := &person{cmd: cmd, pty: f, done: make(chan struct{})}
	go func() {
		_, _ = io.Copy(io.Discard, f) // drain the screen so tmux never blocks
		_ = cmd.Wait()
		close(p.done)
	}()
	return p
}

func (p *person) close() {
	_, _ = p.pty.WriteString("\x02d") // C-b d: detach
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	_ = p.pty.Close()
}

func waitCapture(t *testing.T, r *running, pane tmux.PaneID, ok func([]string) bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		c, err := r.Capture(ctxT(t), pane)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if ok(c.Lines) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never showed what the test waits for: %q", pane, c.Lines)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitPanes(t *testing.T, r *running, what string, ok func([]tmux.Pane) bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		panes, err := r.ListPanes(ctxT(t))
		if err != nil {
			t.Fatalf("ListPanes: %v", err)
		}
		if ok(panes) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s: %+v", what, panes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// findEvent returns the last log event named msg, failing when there is none.
func findEvent(t *testing.T, logs *lockedBuffer, msg string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, ev := range logs.events(t) {
		if ev["msg"] == msg {
			found = ev
		}
	}
	if found == nil {
		t.Fatalf("no %s log event in:\n%s", msg, logs.String())
	}
	return found
}

// waitEvent waits for a log event named msg holding every field in want.
func waitEvent(t *testing.T, logs *lockedBuffer, msg string, want map[string]any) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		for _, ev := range logs.events(t) {
			match := ev["msg"] == msg
			for k, v := range want {
				match = match && ev[k] == v
			}
			if match {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s %v in:\n%s", msg, want, logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitTrue waits until ok holds.
func waitTrue(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("no %s within %s", what, wait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// exitReasons lists the reason of every tmux.exit log event, in order.
func exitReasons(t *testing.T, logs *lockedBuffer) []string {
	t.Helper()
	var out []string
	for _, ev := range logs.events(t) {
		if ev["msg"] == "tmux.exit" {
			r, _ := ev["reason"].(string)
			out = append(out, r)
		}
	}
	return out
}

// waitEventCount waits until at least n log events are named msg.
func waitEventCount(t *testing.T, logs *lockedBuffer, msg string, n int) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		got := 0
		for _, ev := range logs.events(t) {
			if ev["msg"] == msg {
				got++
			}
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d %s events, want %d:\n%s", got, msg, n, logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustRunner(t *testing.T) *proc.Runner {
	t.Helper()
	r, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
