package inventory

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// A dead pane whose exit status lands after the death was read is a
// PaneChanged: the hub already has the status-less PaneExited, and the
// status alone must still reach it (on Linux no notice follows the status
// alone, so the observer re-reads on a timer until it lands).
func TestDiffDeadStatusLandingEmitsChanged(t *testing.T) {
	t.Parallel()
	old := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "w"}}, Panes: []tmux.PaneInfo{pane(0, 0, 0, 0), pane(0, 0, 1, 1)}}
	old.Panes[1].Dead = true // dead with no status yet: tmux has not reaped the child
	next := tmux.Inventory{StartID: "s", Sessions: []tmux.SessionInfo{{ID: 0, Name: "w"}}, Panes: []tmux.PaneInfo{old.Panes[0], pane(0, 0, 1, 1)}}
	next.Panes[1].Dead, next.Panes[1].DeadStatus = true, 5
	if got := describe(diff(buildServer("default", old, ""), buildServer("default", next, ""))); got != "changed %1" {
		t.Fatalf("status landing = %q, want %q", got, "changed %1")
	}
	// Still no status: nothing to report.
	if got := describe(diff(buildServer("default", old, ""), buildServer("default", old, ""))); got != "" {
		t.Fatalf("still status-less = %q, want no event", got)
	}
}

type collectEmitter struct{ evs []*agentv1.Event }

func (c *collectEmitter) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	c.evs = append(c.evs, ev)
	return uint64(len(c.evs)), nil
}

func deadStatusServer(t *testing.T, tel *telemetry.Telemetry) (*server, *collectEmitter) {
	t.Helper()
	ot, err := newObsTelemetry(tel)
	if err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	return &server{o: &Observer{tel: ot, opts: Options{Emitter: em}}, name: "default", kind: "default"}, em
}

func deadStatusInventory(status int, dead bool) tmux.Inventory {
	p1 := pane(0, 0, 1, 1)
	p1.Dead, p1.DeadStatus = dead, status
	return tmux.Inventory{
		StartID:  "s",
		Sessions: []tmux.SessionInfo{{ID: 0, Name: "w"}},
		Panes:    []tmux.PaneInfo{pane(0, 0, 0, 0), p1},
	}
}

func scrapeValue(t *testing.T, tel *telemetry.Telemetry, name string, labels ...string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		ok := true
		for _, l := range labels {
			ok = ok && strings.Contains(line, l)
		}
		if ok {
			return line[strings.LastIndexByte(line, ' ')+1:]
		}
	}
	return ""
}

// A status-less remains death is waited on: when the status lands, the
// PaneChanged goes out and the wait counts landed.
func TestDeadStatusWaitLands(t *testing.T) {
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "landed", LogOutput: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	s, em := deadStatusServer(t, tel)
	ctx := context.Background()
	s.apply(ctx, buildServer("default", deadStatusInventory(tmux.NoStatus, false), "3.7c"))
	s.apply(ctx, buildServer("default", deadStatusInventory(tmux.NoStatus, true), "3.7c"))
	if got := describe(em.evs); got != "exited %1 status=? remains=true" {
		t.Fatalf("death = %q, want the status-less exit", got)
	}
	if n := len(s.pending); n != 1 {
		t.Fatalf("pending waits = %d, want 1", n)
	}
	em.evs = nil
	s.apply(ctx, buildServer("default", deadStatusInventory(5, true), "3.7c"))
	if got := describe(em.evs); got != "changed %1" {
		t.Fatalf("landing = %q, want one PaneChanged", got)
	}
	if pc := em.evs[0].GetPaneChanged(); !pc.GetPane().GetDead() || pc.GetPane().GetExitStatus() != 5 {
		t.Fatalf("changed pane = %v, want dead with exit status 5", pc.GetPane())
	}
	if len(s.pending) != 0 {
		t.Fatalf("pending waits = %d, want none left", len(s.pending))
	}
	if got := scrapeValue(t, tel, "fleet_agent_tmux_dead_status_polls_total", `server_kind="default"`, `result="landed"`); got != "1" {
		t.Fatalf("dead_status_polls{result=landed} = %q, want 1", got)
	}
}

// A pane that never gains a status (a signal death) is waited on at most
// deadStatusPollMax reads, then counted expired: the wait ends either way.
func TestDeadStatusWaitExpires(t *testing.T) {
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "expired", LogOutput: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	s, em := deadStatusServer(t, tel)
	ctx := context.Background()
	s.apply(ctx, buildServer("default", deadStatusInventory(tmux.NoStatus, false), "3.7c"))
	stillDead := buildServer("default", deadStatusInventory(tmux.NoStatus, true), "3.7c")
	s.apply(ctx, stillDead)
	em.evs = nil
	for range deadStatusPollMax {
		s.apply(ctx, stillDead)
	}
	if len(s.pending) != 0 {
		t.Fatalf("pending waits = %d, want none left after %d reads", len(s.pending), deadStatusPollMax)
	}
	if len(em.evs) != 0 {
		t.Fatalf("re-reads while still status-less emitted %q, want nothing", describe(em.evs))
	}
	if got := scrapeValue(t, tel, "fleet_agent_tmux_dead_status_polls_total", `server_kind="default"`, `result="expired"`); got != "1" {
		t.Fatalf("dead_status_polls{result=expired} = %q, want 1", got)
	}
}

// The wait spans the whole window a status may take, not just the first
// ten re-reads: a status landing late still finds its wait owed (Linux CI:
// the status can arrive after the first seconds, inside the 30 s wait).
func TestDeadStatusWaitSpansTheWindow(t *testing.T) {
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "window", LogOutput: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	s, em := deadStatusServer(t, tel)
	ctx := context.Background()
	s.apply(ctx, buildServer("default", deadStatusInventory(tmux.NoStatus, false), "3.7c"))
	stillDead := buildServer("default", deadStatusInventory(tmux.NoStatus, true), "3.7c")
	s.apply(ctx, stillDead)
	em.evs = nil
	for range 10 {
		s.apply(ctx, stillDead)
	}
	if len(s.pending) != 1 {
		t.Fatalf("pending waits = %d after 10 status-less re-reads, want the wait still owed", len(s.pending))
	}
	for range deadStatusPollMax {
		s.apply(ctx, stillDead)
	}
	if len(s.pending) != 0 {
		t.Fatalf("pending waits = %d after the full window of re-reads, want none left", len(s.pending))
	}
	if got := scrapeValue(t, tel, "fleet_agent_tmux_dead_status_polls_total", `server_kind="default"`, `result="expired"`); got != "1" {
		t.Fatalf("dead_status_polls{result=expired} = %q, want 1", got)
	}
}

// A signal death stays status-less for good: the loop's follow-up reads
// end after deadStatusPollMax, counted expired, with no status event and
// no runaway polling.
func TestDeadStatusPollEndsOnSignalDeath(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachDeadStatusPollEnds(t, b) })
	}
}

func eachDeadStatusPollEnds(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
	dev.Run(t, "set-option", "-g", "remain-on-exit", "on")
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "sigkill", LogOutput: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var evs []*agentv1.Event
	added := make(chan struct{}, 1)
	o, err := New(Options{Program: b.Path, Runner: runner, Telemetry: tel, Folder: folder.Dir, Servers: []string{"default"}, Emitter: emitFunc(func(ev *agentv1.Event) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
		select {
		case added <- struct{}{}:
		default:
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { o.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	s := o.servers["default"]
	wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
	defer wcancel()
	if err := s.c.WaitState(wctx, tmux.StateUp); err != nil {
		t.Fatal(err)
	}
	if err := o.Snapshot(wctx, newSnap()); err != nil { // the cold read is done
		t.Fatal(err)
	}
	out := strings.Fields(dev.Run(t, "split-window", "-d", "-P", "-F", "#{pane_id} #{pane_pid}", "-t", "work:0", "exec sleep 100"))
	if len(out) != 2 {
		t.Fatalf("split-window printed %q, want the pane id and pid", out)
	}
	id := out[0]
	pid, err := strconv.Atoi(out[1])
	if err != nil || pid <= 0 {
		t.Fatalf("pane_pid = %q: %v", out[1], err)
	}
	// tmux prints pane_pid right after its fork, so a kill sent at once
	// can reach the shell before it runs sleep; wait for sleep first.
	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd, err := dev.Try("display-message", "-p", "-t", id, "#{pane_current_command}")
		if err == nil && strings.TrimSpace(cmd) == "sleep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never ran sleep (last %q, err %v)", id, cmd, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	before := s.reads.Load()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil { // our throwaway server's own pane
		t.Fatal(err)
	}
	// The death is a status-less remains exit ...
	deadline = time.Now().Add(30 * time.Second)
	sawExit := false
	for !sawExit {
		mu.Lock()
		for _, ev := range evs {
			if pe := ev.GetPaneExited(); pe != nil && pe.GetPane().GetPaneId() == id {
				if !pe.GetRemains() || pe.ExitStatus != nil {
					mu.Unlock()
					t.Fatalf("signal death exit = %v, want remains true and no status", pe)
				}
				sawExit = true
			}
		}
		mu.Unlock()
		if sawExit {
			break
		}
		select {
		case <-added:
		case <-time.After(30 * time.Second):
			t.Fatalf("no PaneExited for the killed pane %s", id)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no PaneExited for the killed pane %s", id)
		}
	}
	// ... and the wait ends expired: bounded reads, no status event. The
	// window spans the full 30 s of follow-up reads, so this phase allows
	// twice that.
	expired := ""
	deadline = time.Now().Add(60 * time.Second)
	for expired != "1" {
		expired = scrapeValue(t, tel, "fleet_agent_tmux_dead_status_polls_total", `server_kind="default"`, `result="expired"`)
		if time.Now().After(deadline) {
			t.Fatalf("dead_status_polls{result=expired} = %q, want 1", expired)
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range evs {
		switch body := ev.GetBody().(type) {
		case *agentv1.Event_PaneChanged:
			if body.PaneChanged.GetPane().GetKey().GetPaneId() == id && body.PaneChanged.GetPane().ExitStatus != nil {
				t.Fatalf("signal death gained a status: %v", body.PaneChanged.GetPane())
			}
		case *agentv1.Event_PaneExited:
			if body.PaneExited.GetPane().GetPaneId() == id && body.PaneExited.ExitStatus != nil {
				t.Fatalf("signal death gained a status: %v", body.PaneExited)
			}
		}
	}
	// One death read plus the owed follow-ups, with room for a stray
	// notice-driven read while the wait runs.
	if reads := s.reads.Load() - before; reads > int64(deadStatusPollMax)+5 {
		t.Fatalf("%d reads after the kill, want at most %d", reads, deadStatusPollMax+5)
	}
}
