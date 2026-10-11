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
func TestPaneKilledBySignalHasNoStatus(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-option", "-g", "remain-on-exit", "on")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			// tmux prints pane_pid right after its fork, so the kill can reach
			// the shell before it runs sleep. SIGKILL, because no shell can
			// catch it (zsh catches SIGHUP and exits 1).
			out := strings.Fields(srv.Run(t, "new-window", "-P", "-F", "#{pane_id} #{pane_pid}", "-t", "work", "exec sleep 100"))
			if len(out) != 2 {
				t.Fatalf("new-window printed %q, want the pane id and pid", out)
			}
			id := out[0]
			pid, err := strconv.Atoi(out[1])
			if err != nil || pid <= 0 {
				t.Fatalf("pane_pid = %q: %v", out[1], err)
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil { // our throwaway server's own pane
				t.Fatal(err)
			}
			want := ""
			if b.Name == "latest" {
				want = tmux.SignalKill
			}
			// 3.7c marks a pane dead only once tmux has its status, so the
			// signal is in by then; 3.2a never reports one.
			waitPanes(t, r, "pane "+id+" dead", func(panes []tmux.Pane) bool {
				for _, p := range panes {
					if p.ID.String() == id && p.Dead {
						return p.DeadSignal == want
					}
				}
				return false
			})
			for _, p := range mustListPanes(t, r) {
				if p.ID.String() == id && (!p.Dead || p.DeadStatus != tmux.NoStatus || p.DeadSignal != want) {
					t.Fatalf("pane killed by SIGKILL = %+v, want Dead true, NoStatus and DeadSignal %q", p, want)
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
// the wait stops there and names the signal and the pane's pid. A CI run on
// tmux 3.7c once saw "exit 4" end as dead with no status for the whole 30 s,
// which tmux prints only for a signal death (format.c), with nothing to say
// which.
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
			t.Fatalf("no pane %s dead with status %d (%s): %+v", id, want, deathOf(srv, id), panes)
		}
		time.Sleep(20 * time.Millisecond)
	}
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
