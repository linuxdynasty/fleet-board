package tmux_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

var startIDRE = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func TestClientAttachesWithBothFlagsAndReportsUp(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			if !r.Up() {
				t.Fatal("Up() false in StateUp")
			}
			if id := r.ServerStartID(); !startIDRE.MatchString(id) {
				t.Fatalf("ServerStartID = %q, want [a-z0-9-]{1,64}", id)
			}
			flags := srv.Run(t, "list-clients", "-F", "#{client_control_mode} #{client_flags}")
			if !strings.Contains(flags, "1 ") || !strings.Contains(flags, "ignore-size") || !strings.Contains(flags, "pause-after=1") {
				t.Fatalf("control client flags = %q, want ignore-size and pause-after=1", flags)
			}
			if got := series(scrape(t, r.tel), "fleet_agent_tmux_control_up", `server_kind="fleet"`); got != "1" {
				t.Fatalf("fleet_agent_tmux_control_up = %q, want 1", got)
			}
			if n := tmux.ControlDown(r.Client); n != 0 {
				t.Fatalf("ControlDown = %d, want 0", n)
			}
			ev := findEvent(t, r.logs, "tmux.attached")
			if ev["server_kind"] != "fleet" || ev["flags"] != "ignore-size,pause-after=1" {
				t.Fatalf("tmux.attached = %v", ev)
			}
			if v, _ := ev["version"].(string); !regexp.MustCompile(`^3\.[0-9]+[a-z]?$`).MatchString(v) {
				t.Fatalf("tmux.attached version = %q", v)
			}
		})
	}
}

// Stopping the agent ends an attached client: Run returns, the state is
// stopped, the gauge is 0 and tmux.exit says cancelled (a shutdown, not a
// failure).
func TestCancelEndsAnAttachedClient(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			tel, logs := newTelemetry(t)
			c, err := tmux.NewClient(tmux.ClientOptions{Program: b.Path, Socket: srv.Socket, Server: "fleet", Runner: mustRunner(t), Telemetry: tel})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- c.Run(ctx) }()
			if err := c.WaitState(ctxT(t), tmux.StateUp); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run = %v, want context.Canceled", err)
				}
			case <-time.After(wait):
				t.Fatal("Run did not return after its context ended")
			}
			if c.State() != tmux.StateStopped {
				t.Fatalf("state %s, want stopped", c.State())
			}
			if got := series(scrape(t, tel), "fleet_agent_tmux_control_up", `server_kind="fleet"`); got != "0" {
				t.Fatalf("fleet_agent_tmux_control_up = %q, want 0", got)
			}
			if reasons := exitReasons(t, logs); len(reasons) != 1 || reasons[0] != "cancelled" {
				t.Fatalf("tmux.exit reasons = %q, want [cancelled]", reasons)
			}
			waitTrue(t, "the control client to leave the server", func() bool {
				return strings.TrimSpace(srv.Run(t, "list-clients", "-F", "#{client_control_mode}")) == ""
			})
		})
	}
}

func TestSendTypesTmuxKeyNamesAsLiteralText(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec cat")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			pane := firstPane(t, r, 0)
			text := `Enter C-c Escape Up \; kill-server ; display #{pane_id} $HOME '`
			if err := r.Send(ctxT(t), literal(t, r.paneKey(pane.ID), text)); err != nil {
				t.Fatalf("Send literal: %v", err)
			}
			if err := r.Send(ctxT(t), keys(t, r.paneKey(pane.ID), typing.KeyEnter)); err != nil {
				t.Fatalf("Send Enter: %v", err)
			}
			// cat prints the line back after Enter: the tty echo and cat's copy.
			waitCapture(t, r, pane.ID, func(lines []string) bool {
				n := 0
				for _, l := range lines {
					if l == text {
						n++
					}
				}
				return n == 2
			})
			if !srv.Alive() {
				t.Fatal("the server died: text was read as a command")
			}
		})
	}
}

func TestSendRefusesTheZeroLineAndALineForAnotherServer(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			pane := firstPane(t, r, 0)

			if err := r.Send(ctxT(t), typing.Line{}); !errors.Is(err, typing.ErrZeroLine) {
				t.Fatalf("Send(zero Line) = %v, want ErrZeroLine", err)
			}
			other := r.paneKey(pane.ID)
			other.ServerStartId = "not-this-start"
			if err := r.Send(ctxT(t), literal(t, other, "nope")); !errors.Is(err, tmux.ErrWrongServer) {
				t.Fatalf("Send(other start id) = %v, want ErrWrongServer", err)
			}
			otherServer := r.paneKey(pane.ID)
			otherServer.Server = "default"
			if err := r.Send(ctxT(t), literal(t, otherServer, "nope")); !errors.Is(err, tmux.ErrWrongServer) {
				t.Fatalf("Send(other server) = %v, want ErrWrongServer", err)
			}
			c, err := r.Capture(ctxT(t), pane.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(c.Lines, "\n"), "nope") {
				t.Fatal("a refused line reached the pane")
			}
		})
	}
}

func TestBurstOfOutputBeyondOneRead(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			n := newNotes()
			r := startClient(t, srv, n)
			r.waitState(t, tmux.StateUp)
			const size = 256 << 10
			srv.Run(t, "new-window", "-t", "work", "head -c 262144 /dev/zero | tr '\\000' a; echo; echo DONE-MARK; exec cat")
			var pane tmux.PaneID = -1
			got, paused := 0, false
			tail := ""
			n.waitFor(t, 0, "the end of the burst or a pause", func(m tmux.Message) bool {
				switch m := m.(type) {
				case tmux.Output:
					if pane < 0 {
						pane = m.Pane
					}
					if m.Pane == pane {
						got += strings.Count(string(m.Data), "a")
						tail = lastN(tail+string(m.Data), 64)
					}
					return strings.Contains(tail, "DONE-MARK")
				case tmux.Pause:
					paused = true
					return true
				case tmux.Discarded:
					t.Fatalf("the parser discarded part of the burst: %+v", m)
				}
				return false
			})
			if !paused && got != size {
				t.Fatalf("got %d bytes of the burst, want %d", got, size)
			}
			if paused {
				t.Logf("tmux paused the pane after %d bytes (pause-after); the burst still parsed", got)
			}
			// The stream is still in step: a command after the burst answers.
			if _, err := r.ListPanes(ctxT(t)); err != nil {
				t.Fatalf("ListPanes after the burst: %v", err)
			}
		})
	}
}

func lastN(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
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
			waitPanes(t, r, "pane "+id+" dead with status 4", func(panes []tmux.Pane) bool {
				for _, p := range panes {
					if p.ID.String() == id {
						return p.Dead && p.DeadStatus == 4
					}
				}
				return false
			})
			// A live pane reports no status.
			live := firstPane(t, r, 0)
			if live.Dead || live.DeadStatus != tmux.NoStatus {
				t.Fatalf("live pane = %+v, want Dead false and NoStatus", live)
			}
		})
	}
}

func TestWindowRenameArrives(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			n := newNotes()
			r := startClient(t, srv, n)
			r.waitState(t, tmux.StateUp)
			win := firstPane(t, r, 0).Window
			srv.Run(t, "rename-window", "-t", win.String(), "two  words")
			n.waitFor(t, 0, "%window-renamed", func(m tmux.Message) bool {
				w, ok := m.(tmux.WindowRenamed)
				return ok && w.Window == win && w.Name == "two  words"
			})
			if strings.Contains(r.logs.String(), "two  words") {
				t.Fatal("a window name reached a log line")
			}
		})
	}
}

func TestNestedLoopSubscriptionIsConfirmed(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "a", "exec cat")
			srv.NewSession(t, "-s", "b", "exec cat")
			n := newNotes()
			r := startClient(t, srv, n)
			r.waitState(t, tmux.StateUp)
			err := r.Subscribe(ctxT(t), tmux.Subscription{Name: "win-act", Format: "#{S:#{W:#{window_id}=#{window_activity} }}"})
			if err != nil {
				t.Fatal(err)
			}
			// Both sessions' windows, expanded (3.2a prints the same as 3.7c;
			// the per-window fallback of the row-7 ruling fork 5(e) is not
			// needed).
			re := regexp.MustCompile(`^@0=[0-9]+ @1=[0-9]+ $`)
			n.waitFor(t, 0, "the nested-loop value", func(m tmux.Message) bool {
				s, ok := m.(tmux.SubscriptionChanged)
				return ok && s.Name == "win-act" && re.MatchString(s.Value)
			})
			waitEvent(t, r.logs, "tmux.subscription_confirmed", map[string]any{"name": "win-act"})
		})
	}
}

func TestSubscribeRefusesWhatWouldBreakTheCommand(t *testing.T) {
	c, err := tmux.NewClient(tmux.ClientOptions{Program: "tmux", Socket: "/nonexistent/s", Server: "fleet", Runner: mustRunner(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []tmux.Subscription{
		{Name: "", Format: "#{pane_id}"},
		{Name: "has space", Format: "#{pane_id}"},
		{Name: "colon:name", Format: "#{pane_id}"},
		{Name: "ok", What: "work", Format: "#{pane_id}"},
		{Name: "ok", What: "%1;x", Format: "#{pane_id}"},
		{Name: "ok", Format: ""},
		{Name: "ok", Format: "it's"},
		{Name: "ok", Format: "a\nkill-server"},
	} {
		if err := c.Subscribe(ctxT(t), s); !errors.Is(err, tmux.ErrBadSubscription) {
			t.Errorf("Subscribe(%q, %q, %q) = %v, want ErrBadSubscription", s.Name, s.What, s.Format, err)
		}
	}
}
