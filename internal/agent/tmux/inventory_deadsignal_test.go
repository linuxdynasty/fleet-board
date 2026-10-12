package tmux_test

import (
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// The inventory read carries pane_dead_signal right after pane_dead_status
// and reads it by the ListPanes rule: only for a dead pane, through the
// closed set, never tmux's text; a status and a signal both present is not
// tmux's shape. The fields after it (width, height, the attach_view flag)
// are read one place later.
func TestInventoryRecordReadsTheDeadSignal(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	cases := []struct {
		name                 string
		dead, status, signal string
		wantStatus           int
		wantSignal           string
		bad                  bool
	}{
		{"live pane, empty signal", "0", "", "", tmux.NoStatus, "", false},
		{"exited 3", "1", "3", "", 3, "", false},
		{"killed, macos name", "1", "", "kill", tmux.NoStatus, tmux.SignalKill, false},
		{"killed, linux number", "1", "", "15", tmux.NoStatus, tmux.SignalTerm, false},
		{"a name this OS lacks", "1", "", "iot", tmux.NoStatus, tmux.SignalOther, false},
		{"3.2a, dead with neither", "1", "", "", tmux.NoStatus, "", false},
		{"a live pane drops a signal", "0", "", "9", tmux.NoStatus, "", false},
		{"both a status and a signal", "1", "4", "9", 0, "", true},
		{"both on a live pane", "0", "4", "9", 0, "", true},
		{"a status that is not a number", "1", "x", "", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRec("%1", [nText]string{tSessionName: "work", tWindowName: "zsh", tCommand: "zsh"})
			r.fixed[fDead], r.fixed[fDeadStatus], r.fixed[fDeadSignal] = tc.dead, tc.status, tc.signal
			r.fixed[fWidth], r.fixed[fHeight], r.fixed[fAttachView] = "100", "30", "1"
			body := strings.Split(r.line(marker)+"\n"+record(marker, "%2", "g"), "\n")
			panes, _, ok := tmux.InventoryRecordsForTest(body, marker)
			if tc.bad {
				if ok {
					t.Fatalf("read %+v; want the reply refused", panes)
				}
				return
			}
			if !ok || len(panes) != 2 {
				t.Fatalf("ok %v, %d panes; want 2", ok, len(panes))
			}
			p := panes[0]
			if p.Dead != (tc.dead == "1") || p.DeadStatus != tc.wantStatus || p.DeadSignal != tc.wantSignal {
				t.Fatalf("pane = Dead %v DeadStatus %d DeadSignal %q; want %v, %d, %q", p.Dead, p.DeadStatus, p.DeadSignal, tc.dead == "1", tc.wantStatus, tc.wantSignal)
			}
			if p.Width != 100 || p.Height != 30 || !p.Tags.AttachView {
				t.Fatalf("pane = %dx%d attach view %v; want 100x30 and true", p.Width, p.Height, p.Tags.AttachView)
			}
			if q := panes[1]; q.Width != 80 || q.Height != 24 || q.Tags.AttachView || q.DeadSignal != "" {
				t.Fatalf("second pane = %+v; want 80x24, no attach view, no signal", q)
			}
		})
	}
}

// A size or attach_view flag that is not a number or a flag still fails the
// record at its new place: the shift moved the checks with the fields.
func TestInventoryRecordChecksTheShiftedFields(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	for name, at := range map[string]int{"width": fWidth, "height": fHeight, "attach view": fAttachView} {
		r := newRec("%1", [nText]string{tSessionName: "work"})
		r.fixed[at] = "x"
		if _, _, ok := tmux.InventoryRecordsForTest([]string{r.line(marker)}, marker); ok {
			t.Errorf("%s of x was read; want the reply refused", name)
		}
	}
}

// Live, on both builds: a pane a signal killed shows the signal in the
// inventory read (3.7c; 3.2a has no pane_dead_signal and reports none), a
// pane that exited has its status and no signal, and a live pane has
// neither.
func TestReadInventoryDeadSignal(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-option", "-g", "remain-on-exit", "on")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			names := map[syscall.Signal]string{syscall.SIGKILL: tmux.SignalKill, syscall.SIGTERM: tmux.SignalTerm}
			killed := map[string]string{}
			for sig, name := range names {
				id := killPane(t, srv, sig)
				killed[id] = name
				if b.Name != "latest" {
					killed[id] = "" // 3.2a has no pane_dead_signal
				}
			}
			exited := strings.TrimSpace(srv.Run(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "work:", "exit 3"))
			inv := readUntil(t, r, func(inv tmux.Inventory) bool {
				dead := 0
				for _, p := range inv.Panes {
					if want, ok := killed[p.ID.String()]; ok && p.Dead && p.DeadSignal == want {
						dead++
					}
					if p.ID.String() == exited && p.Dead && p.DeadStatus == 3 {
						dead++
					}
				}
				return dead == len(killed)+1
			})
			for _, p := range inv.Panes {
				id := p.ID.String()
				switch want, ok := killed[id]; {
				case ok:
					if !p.Dead || p.DeadStatus != tmux.NoStatus || p.DeadSignal != want {
						t.Errorf("killed pane %s = Dead %v DeadStatus %d DeadSignal %q; want true, NoStatus, %q", id, p.Dead, p.DeadStatus, p.DeadSignal, want)
					}
				case id == exited:
					if !p.Dead || p.DeadStatus != 3 || p.DeadSignal != "" {
						t.Errorf("exited pane %s = Dead %v DeadStatus %d DeadSignal %q; want true, 3, no signal", id, p.Dead, p.DeadStatus, p.DeadSignal)
					}
				default:
					if p.Dead || p.DeadStatus != tmux.NoStatus || p.DeadSignal != "" || p.Width <= 0 || p.Height <= 0 {
						t.Errorf("live pane %s = %+v; want live, NoStatus, no signal and a size", id, p)
					}
				}
			}
		})
	}
}

// killPane opens a window running sleep and sends its program sig; it
// returns the pane id. The pane stays (remain-on-exit). It waits until the
// pane runs sleep first: killing its pid at once can hit the shell before it
// execs, and the pane then sits dead with no status and no signal (#216).
func killPane(t *testing.T, srv *tmuxtest.Server, sig syscall.Signal) string {
	t.Helper()
	out := strings.Fields(srv.Run(t, "new-window", "-d", "-P", "-F", "#{pane_id} #{pane_pid}", "-t", "work:", "exec sleep 100"))
	if len(out) != 2 {
		t.Fatalf("new-window printed %q, want the pane id and pid", out)
	}
	pid, err := strconv.Atoi(out[1])
	if err != nil || pid <= 0 {
		t.Fatalf("pane_pid = %q: %v", out[1], err)
	}
	waitPaneCommand(t, srv, out[0], "sleep")
	if err := syscall.Kill(pid, sig); err != nil { // our throwaway server's own pane
		t.Fatal(err)
	}
	return out[0]
}
