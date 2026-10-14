package inventory_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Proof 5: kill the control client and press a key during the gap. After
// reconnect the key is reported (the reattach read compares against the
// last seen times); the backup poll runs until the subscription confirms,
// then stops. The first %subscription-changed arrives right after
// registration on both versions (the proof of life), so the poll usually
// fires only a few times.
func TestReconnectReportsGapKeysAndPollStops(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			dev.NewSession(t, "-s", "other", "exec cat")
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
				inventory.SetInputPollForTest(o, 100*time.Millisecond)
				inventory.SetInputGapForTest(o, time.Second)
			}, "default")
			r.waitState(t, "default", tmux.StateUp)
			ids := paneIDs(t, r)
			other := ids["$1"]

			// The backup poll runs while unconfirmed: reads accumulate
			// until the first value confirms the subscription.
			waitConfirmed(t, r, "fleet-input", 1)
			if n := inputReads(t, r, "default"); n < 1 {
				t.Fatalf("input reads = %d before confirm, want at least the attach read", n)
			}
			// ... and then stops: the reads quiesce while idle with no
			// typing (an in-flight poll read may still land just after
			// the confirm, so this waits for 2 s of stillness).
			quiesceInputReads(t, r, "default", 2*time.Second)

			// A person attached past the 1 s test gate, so the gap key
			// is due an event whenever it is reported. (The 10 s
			// contract gate is proved in TestPersonTyping and pinned
			// by TestInputGapDefault.)
			p := startTyper(t, dev, "other")
			time.Sleep(2500 * time.Millisecond)
			base := len(r.rec.all())

			// Kill the control client's session: %exit, backoff, reattach.
			// The key lands while the client is down.
			dev.Run(t, "kill-session", "-t", "work")
			waitDown(t, r)
			t0 := p.key(t, "g")
			r.waitState(t, "default", tmux.StateUp)
			ev, _ := paneInput(t, r, base, "the gap key", other)
			if dt := time.Since(t0); dt > 10*time.Second {
				t.Fatalf("gap PaneInput took %v, want it at the reattach read", dt)
			}
			if got := ev.GetPaneInput().GetInputTime(); got == nil || time.Since(got.AsTime()) > time.Since(t0)+5*time.Second {
				t.Fatalf("gap PaneInput time = %v, want the key just typed", got)
			}
			waitConfirmed(t, r, "fleet-input", 2)
		})
	}
}

// waitConfirmed waits for n tmux.subscription_confirmed log events for
// the named subscription.
func waitConfirmed(t *testing.T, r *obsRig, name string, n int) {
	t.Helper()
	count := func() int {
		got := 0
		for _, ev := range r.logs.named("tmux.subscription_confirmed") {
			if ev["name"] == name {
				got++
			}
		}
		return got
	}
	deadline := time.Now().Add(wait)
	for {
		if count() >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d subscription_confirmed events for %s, want %d:\n%s", count(), name, n, r.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// quiesceInputReads waits until no client read ends for still, so the
// test can say the backup poll stopped: while unconfirmed a read ends
// every poll gap, so stillness never comes before the deadline.
func quiesceInputReads(t *testing.T, r *obsRig, kind string, still time.Duration) {
	t.Helper()
	last, since := inputReads(t, r, kind), time.Now()
	deadline := time.Now().Add(wait)
	for {
		if got := inputReads(t, r, kind); got != last {
			last, since = got, time.Now()
		}
		if time.Since(since) >= still {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("input reads keep ending (%d): the poll never stopped", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPersonClient waits until a non-control client is listed: the pty
// attach landed, so a key typed now reaches a pane.
func waitPersonClient(t *testing.T, r *obsRig) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		clients, err := r.o.Client("default").ReadClients(ctxT(t))
		if err == nil {
			for _, cl := range clients {
				if !cl.Control {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no person client listed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ctxT bounds one test step.
func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	t.Cleanup(cancel)
	return ctx
}

// waitDown waits until the observer's default client leaves StateUp.
func waitDown(t *testing.T, r *obsRig) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for r.o.Client("default").State() == tmux.StateUp {
		if time.Now().After(deadline) {
			t.Fatal("the control client never left StateUp")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitSession waits until the observer's default client is attached to
// session want. Killing its session ends the attach with %exit and the
// client lands on the lowest session left, but the drop in between is not
// guaranteed to show between two 20 ms polls on a loaded machine, so the
// wait is for the landed session, not for the drop. A failure prints the
// recent tmux.exit reasons with the state, so a stuck absent names the
// trajectory instead of only the timeout.
func waitSession(t *testing.T, r *obsRig, want tmux.SessionID) {
	t.Helper()
	c := r.o.Client("default")
	deadline := time.Now().Add(wait)
	for {
		if c.Up() && c.Session() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the control client is %s on %s, want up on $%d (exits: %v)",
				c.State(), c.Session(), want, r.logs.named("tmux.exit"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Proof 6 (first half): over 60 s idle with a person attached, the agent
// issues zero list-clients commands and sends zero events. (Proof 1
// covers the second half: steady typing is at most one PaneInput per pane
// per 10 s.)
func TestIdleServerCostsNoClientReads(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel() // the 60 s windows overlap: both versions idle together
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			startTyper(t, dev, "work")
			waitConfirmed(t, r, "fleet-input", 1)
			quiesceInputReads(t, r, "default", 2*time.Second)
			n, base := inputReads(t, r, "default"), len(r.rec.all())

			time.Sleep(60 * time.Second)
			if got := inputReads(t, r, "default"); got != n {
				t.Fatalf("input reads = %d, want %d: no list-clients while idle", got, n)
			}
			for _, ev := range r.rec.all()[base:] {
				if ev.GetPaneInput() != nil {
					t.Fatalf("PaneInput while idle: %+v", ev.GetPaneInput())
				}
			}
		})
	}
}

// Proof 7: drop every %subscription-changed line. The on-demand read still
// reports a fresh key, so a missed push cannot open the gate: with no
// subscription registered no push can arrive at all, and CheckInput reads
// list-clients at that moment. It stamps the level and emits nothing.
func TestCheckInputReportsKeysWithNoPush(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			// An observer that never runs: only its client's own Run
			// starts, so no subscription is ever registered and no push
			// can arrive. (startObserver would run the loop.)
			logs := &logBuf{}
			tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "b", LogOutput: logs})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := proc.NewRunner(nil)
			if err != nil {
				t.Fatal(err)
			}
			o, err := inventory.New(inventory.Options{
				Program: b.Path, Runner: runner, Telemetry: tel,
				Emitter: newRecorder(t), Folder: folder.Dir, Servers: []string{"default"},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
			r := &obsRig{o: o, rec: newRecorder(t), tel: tel, logs: logs}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { _ = r.o.Client("default").Run(ctx); close(done) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(wait):
					t.Error("client Run did not return")
				}
			}()
			r.waitState(t, "default", tmux.StateUp)
			typer := startTyper(t, dev, "work")
			waitPersonClient(t, r)

			if _, err := r.o.CheckInput(context.Background(), "nope", "%0"); err == nil {
				t.Fatal("CheckInput on an unknown server wants an error")
			}
			t0 := typer.key(t, "k")
			at, err := r.o.CheckInput(ctx, "default", "%0")
			if err != nil {
				t.Fatalf("CheckInput: %v", err)
			}
			if at.IsZero() || time.Since(at) > 10*time.Second || at.Before(t0.Add(-2*time.Second)) {
				t.Fatalf("CheckInput = %v, want the key just typed", at)
			}
			// Nothing was emitted, and a second read keeps the same time:
			// the key is reported once, then the baseline moves on.
			for _, ev := range r.rec.all() {
				if ev.GetPaneInput() != nil {
					t.Fatalf("CheckInput emitted: %+v", ev.GetPaneInput())
				}
			}
			again, err := r.o.CheckInput(ctx, "default", "%0")
			if err != nil {
				t.Fatalf("second CheckInput: %v", err)
			}
			if !again.Equal(at) {
				t.Fatalf("second CheckInput = %v, want %v: no new key, no new time", again, at)
			}
		})
	}
}

// Row-7 ruling 7c2b point 7: a switch by the observer credits no input.
// The observer fallback-attaches to the only (internal) session; the first
// developer session moves it there with switch-client -E. The switch moves
// the tripwire, so the client read runs, and control clients are ignored,
// so it credits nothing.
func TestObserverSwitchCreditsNoInput(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "inner", "exec cat")
			dev.Run(t, "set-option", "-t", "inner", "@fleet_internal", "attach_view")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			if got := r.o.Client("default").Session(); got != 0 {
				t.Fatalf("attached to %s, want the internal $0: nothing else exists", got)
			}
			before := inputReads(t, r, "default")

			dev.NewSession(t, "-s", "dev", "exec cat")
			deadline := time.Now().Add(wait)
			for r.o.Client("default").Session() != 1 {
				if time.Now().After(deadline) {
					t.Fatal("the observer never switched to the developer session")
				}
				time.Sleep(20 * time.Millisecond)
			}
			// The switch moved the tripwire: the read ran...
			deadline = time.Now().Add(wait)
			for inputReads(t, r, "default") == before && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if inputReads(t, r, "default") == before {
				t.Fatal("no client read followed the observer's switch")
			}
			// ... and credited nothing.
			time.Sleep(3 * time.Second)
			for _, ev := range r.rec.all() {
				if ev.GetPaneInput() != nil {
					t.Fatalf("PaneInput for the observer's own switch: %+v", ev.GetPaneInput())
				}
			}
		})
	}
}

// Proof 4: a key through a grouped view session is credited to the real
// pane, and the view session stays internal (reported, never driven).
func TestGroupedViewCreditsTheRealPane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
				inventory.SetInputGapForTest(o, time.Second)
			}, "default")
			r.waitState(t, "default", tmux.StateUp)
			ids := paneIDs(t, r)
			work := ids["$0"]

			// A grouped view of work's window, marked as the fleet's
			// attach view: internal, still reported.
			dev.Run(t, "new-session", "-d", "-t", "work", "-s", "view", "-x", "100", "-y", "30")
			dev.Run(t, "set-option", "-t", "view", "@fleet_internal", "attach_view")
			p := startTyper(t, dev, "view")
			time.Sleep(2500 * time.Millisecond) // past the 1 s test gate
			base := len(r.rec.all())

			p.key(t, "v")
			ev, _ := paneInput(t, r, base, "input through the view", work)
			if got := ev.GetPaneInput().GetPane().GetPaneId(); got != work {
				t.Fatalf("PaneInput pane = %s, want the real pane %s", got, work)
			}

			snap := r.snapshot(t)
			for _, srv := range snap.GetTmuxServers() {
				for _, s := range srv.GetSessions() {
					if s.GetTmuxId() == "$1" && !s.GetInternal() {
						t.Fatalf("the view session is not marked internal: %+v", s)
					}
				}
			}
			if ctrl := r.o.Client("default").Session().String(); ctrl != "$0" {
				t.Fatalf("the control client sits on %s, want $0 (work): the view stays a view", ctrl)
			}
		})
	}
}
