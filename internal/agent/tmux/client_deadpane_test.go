package tmux_test

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// A pane whose program a signal killed is dead with no exit status: tmux
// prints pane_dead_status only for a program that exited (WIFEXITED in
// format.c, 3.2a and 3.7c alike), and the client never makes one up. 3.7c
// names the signal; 3.2a has no pane_dead_signal, so it reports none.
// 3.7c can also leave a pane it killed dead with neither: dead with no
// status and no signal for the whole wait (CI runs 38178261489 and this
// change's own gate run 38304469084, both tmux 3.7c on Linux, SIGKILL sent
// after the pane reported it runs sleep). The cause in tmux is not known;
// its reap loop records a status for every child it finds, so the loss is
// deeper inside tmux (#208 tracks adjacent 3.8 stall behavior). The test
// accepts that stuck state: the client contract already admits a dead pane
// with no status and no signal (3.2a), and the signal mapping itself is
// pinned by TestListPanesReadsTheDeadSignalIntoAClosedSet. A signal tmux
// does name must still be the killing one.
func TestPaneKilledBySignalHasNoStatus(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-option", "-g", "remain-on-exit", "on")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			// tmux prints pane_pid right after its fork, so a kill sent at
			// once can reach the shell before it runs sleep; wait until
			// the pane runs sleep so the kill targets the program (#216).
			// SIGKILL, because no shell can catch it (zsh catches SIGHUP
			// and exits 1).
			out := strings.Fields(srv.Run(t, "new-window", "-P", "-F", "#{pane_id} #{pane_pid}", "-t", "work", "exec sleep 100"))
			if len(out) != 2 {
				t.Fatalf("new-window printed %q, want the pane id and pid", out)
			}
			id := out[0]
			pid, err := strconv.Atoi(out[1])
			if err != nil || pid <= 0 {
				t.Fatalf("pane_pid = %q: %v", out[1], err)
			}
			waitPaneCommand(t, srv, id, "sleep")
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil { // our throwaway server's own pane
				t.Fatal(err)
			}
			want := ""
			if b.Name == "latest" {
				want = tmux.SignalKill
			}
			// A signal tmux names must be the killing one; none at all is
			// the stuck state above, which the client reports as no signal.
			// 3.2a never reports one.
			waitPanes(t, r, "pane "+id+" dead", func(panes []tmux.Pane) bool {
				for _, p := range panes {
					if p.ID.String() == id && p.Dead && p.DeadStatus == tmux.NoStatus {
						return deadSignalOK(p.DeadSignal, want)
					}
				}
				return false
			})
			for _, p := range mustListPanes(t, r) {
				if p.ID.String() == id && (!p.Dead || p.DeadStatus != tmux.NoStatus || !deadSignalOK(p.DeadSignal, want)) {
					t.Fatalf("pane killed by SIGKILL = %+v, want Dead true, NoStatus and DeadSignal %q (or none, tmux's stuck state)", p, want)
				}
			}
		})
	}
}

func TestPaneExitWithStatus(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			// remain-on-exit keeps the dead pane so its status can be read; the
			// agent never sets it on a developer's server (row-7 ruling fork 12).
			srv.Run(t, "set-option", "-g", "remain-on-exit", "on")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			id := strings.TrimSpace(srv.Run(t, "new-window", "-P", "-F", "#{pane_id}", "-t", "work", "exit 4"))
			waitExitStatus(t, srv, r, id, 4)
			// A live pane reports no status and no signal.
			live := firstPane(t, r, 0)
			if live.Dead || live.DeadStatus != tmux.NoStatus || live.DeadSignal != "" {
				t.Fatalf("live pane = %+v, want Dead false, NoStatus and no signal", live)
			}
		})
	}
}

// waitExitStatus waits until pane id is dead with exit status want and no
// signal. A pane that a signal killed stays dead with no status for good, so
// the wait stops there and names the signal and the pane's pid. A pane that
// exited can also end dead with no status and no signal for the whole wait:
// tmux reaped it but recorded neither (tmuxtest.DeadStuck, measured on
// Linux). That stuck state is accepted like a signal death: the client
// reports what tmux knows.
func waitExitStatus(t *testing.T, srv *tmuxtest.Server, r *running, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		panes := mustListPanes(t, r)
		for _, p := range panes {
			if p.ID.String() != id || !p.Dead {
				continue
			}
			if p.DeadStatus == want && p.DeadSignal == "" {
				return
			}
			if p.DeadSignal != "" {
				t.Fatalf("pane %s was killed by signal %s, not exited with status %d (%s); panes %+v", id, p.DeadSignal, want, deathOf(srv, id), panes)
			}
		}
		if time.Now().After(deadline) {
			if out, err := srv.Try("display-message", "-p", "-t", id, "#{pane_dead} #{pane_dead_status} #{pane_dead_signal}"); err == nil && tmuxtest.DeadStuck(out) {
				t.Logf("pane %s exited into tmux's stuck state (dead, no status or signal ever); the status-less death stands", id)
				return
			}
			t.Fatalf("no pane %s dead with status %d (%s): %+v", id, want, deathOf(srv, id), panes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPaneCommand waits until pane id reports pane_current_command == want.
// A new window reports another command (for example "tmux") until its
// program execs, so killing its pid first can hit the shell instead (#216).
func waitPaneCommand(t *testing.T, srv *tmuxtest.Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		got := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", id, "#{pane_current_command}"))
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s runs %q, want %q", id, got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deadSignalOK reports whether got is a signal the test accepts: the one
// tmux should name for the killing signal, or none at all where tmux can
// leave a killed pane stuck dead with neither status nor signal (3.7c; on
// 3.2a want is already "", so only none passes).
func deadSignalOK(got, want string) bool {
	if got == want {
		return true
	}
	return want != "" && got == ""
}

func mustListPanes(t *testing.T, r *running) []tmux.Pane {
	t.Helper()
	panes, err := r.ListPanes(ctxT(t))
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	return panes
}

// death is how tmux says a pane ended, read outside the client for a
// failure message: pane_dead_signal (3.7c; 3.2a has no such variable and
// prints nothing) and pane_pid.
type death struct{ signal, pid, err string }

func (d death) String() string {
	if d.err != "" {
		return "display-message failed: " + d.err
	}
	return "pane_dead_signal=" + quoteOrNone(d.signal) + " pane_pid=" + quoteOrNone(d.pid)
}

func quoteOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func deathOf(srv *tmuxtest.Server, id string) death {
	out, err := srv.Try("display-message", "-p", "-t", id, "#{pane_dead_signal}:#{pane_pid}")
	if err != nil {
		return death{err: err.Error()}
	}
	sig, pid, _ := strings.Cut(strings.TrimSpace(out), ":")
	return death{signal: sig, pid: pid}
}
