package conn_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// 6a (ruling fork 12 c): a Command or Settings goes to its hook with the
// epoch of the stream it came on; the loop sends nothing for it itself.
func TestCommandsAndSettingsReachTheirHooksWithTheStreamsEpoch(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	var got hookLog
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.Command = func(_ context.Context, epoch uint64, c *agentv1.Command) bool {
			got.add("command %s epoch %d", c.GetCommandId().GetValue(), epoch)
			return true
		}
		o.Settings = func(_ context.Context, epoch uint64, s *agentv1.Settings) bool {
			got.add("settings %d epoch %d", s.GetGeneration(), epoch)
			return true
		}
	})
	h.connected(t, 1)
	h.pushCommand(t, "c1", time.Now().Add(time.Hour))
	h.pushSettings(t, 3)
	waitFor(t, 5*time.Second, "both hooks", func() bool { return len(got.get()) == 2 })
	h.dropLive(t)
	h.connected(t, 2)
	h.pushCommand(t, "c2", time.Now().Add(time.Hour))
	waitFor(t, 5*time.Second, "the second command", func() bool { return len(got.get()) == 3 })
	want := []string{"command c1 epoch 1", "settings 3 epoch 1", "command c2 epoch 2"}
	if g := got.get(); !slices.Equal(g, want) {
		t.Errorf("the hooks saw %q, want %q", g, want)
	}
	for n := 1; n <= 2; n++ {
		if q := h.queued(n); !slices.Equal(q, []string{"snapshot"}) {
			t.Errorf("stream %d got %q; the loop must send nothing for a handed-over message", n, q)
		}
	}
	if ev := a.events(t, "connect.message_ignored"); len(ev) != 0 {
		t.Errorf("a message with a hook was logged as ignored: %v", ev)
	}
}

// With no hook registered the kind is logged and ignored, as before row 6.
func TestAKindWithNoHookIsLoggedAndIgnored(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	startLoop(t, a, nil)
	h.connected(t, 1)
	h.pushCommand(t, "c1", time.Now().Add(time.Hour))
	h.pushSettings(t, 1)
	waitFor(t, 5*time.Second, "two ignored lines", func() bool {
		return len(a.events(t, "connect.message_ignored")) == 2
	})
	var kinds []string
	for _, e := range a.events(t, "connect.message_ignored") {
		k, _ := e["kind"].(string)
		kinds = append(kinds, k)
	}
	if !slices.Equal(kinds, []string{"command", "settings"}) {
		t.Errorf("ignored kinds %q, want command then settings", kinds)
	}
}

// A hook whose intake is full refuses the message: it is dropped unlogged
// and unacked (the hub re-sends it), the drop is logged and counted, and
// the stream stays up.
func TestAFullIntakeDropsTheMessageAndCountsIt(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Command = func(context.Context, uint64, *agentv1.Command) bool { return false }
		o.Settings = func(context.Context, uint64, *agentv1.Settings) bool { return false }
	})
	h.connected(t, 1)
	h.pushCommand(t, "c1", time.Now().Add(time.Hour))
	h.pushCommand(t, "bad\x1bid", time.Now().Add(time.Hour))
	h.pushSettings(t, 4)
	waitFor(t, 5*time.Second, "three drops", func() bool { return len(a.events(t, "command.intake_full")) == 3 })
	ev := a.events(t, "command.intake_full")
	if ev[0]["kind"] != "command" || ev[0]["command_id"] != "c1" || ev[0]["level"] != "warn" {
		t.Errorf("first drop = %v, want a warning with kind command and command_id c1", ev[0])
	}
	if _, ok := ev[1]["command_id"]; ok {
		t.Errorf("an id with a control character was logged: %v", ev[1])
	}
	if ev[2]["kind"] != "settings" {
		t.Errorf("third drop = %v, want kind settings", ev[2])
	}
	m := scrape(t, a)
	if got := metric(m, "fleet_agent_command_intake_dropped_total", `kind="command"`); got != "2" {
		t.Errorf("dropped commands = %q, want 2", got)
	}
	if got := metric(m, "fleet_agent_command_intake_dropped_total", `kind="settings"`); got != "1" {
		t.Errorf("dropped settings = %q, want 1", got)
	}
	r.running(t)
	if q := h.queued(1); !slices.Equal(q, []string{"snapshot"}) {
		t.Errorf("the hub got %q; a dropped message gets no answer", q)
	}
}

// OnConnected runs once the snapshot and its unacked results are out (the
// hub already holds them when it starts), with the stream's epoch, and a
// result logged and sent from it is not in that snapshot and goes out once,
// after it; OnDisconnected runs once the stream is gone, when Send is
// already refused.
func TestOnConnectedComesAfterTheSnapshotAndItsResults(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	logResult(t, db, "u1", time.Now())
	var got hookLog
	var loop *conn.Loop
	startLoopThen(t, a, func(o *conn.LoopOptions) {
		o.OnConnected = func(ctx context.Context, epoch uint64, clock conn.HubClock) {
			n := int(epoch) // the fake hub's epochs are 1, 2, ...
			held, end := false, time.Now().Add(5*time.Second)
			for !held && time.Now().Before(end) {
				q := h.queued(n)
				held = len(q) >= 2 && q[0] == "snapshot" && q[1] == "result:u1"
				if !held {
					time.Sleep(5 * time.Millisecond)
				}
			}
			_, ok := clock.Now()
			got.add("up %d clock %v hub held the snapshot and u1 %v", epoch, ok, held)
			fresh := fmt.Sprintf("fresh-%d", n)
			if err := logFinished(ctx, db, fresh); err != nil {
				got.add("log: %v", err)
			}
			for _, m := range []*agentv1.ConnectRequest{resultOf(fresh, typev1.Status_STATUS_OK), ackOf("from-up")} {
				if err := loop.Send(ctx, m); err != nil {
					got.add("send: %v", err)
				}
			}
		}
		o.OnDisconnected = func() {
			got.add("down %v", errors.Is(loop.Send(context.Background(), ackOf("late")), conn.ErrNoStream))
		}
	}, func(l *conn.Loop) { loop = l })
	h.connected(t, 1)
	waitFor(t, 5*time.Second, "the ack sent from OnConnected", func() bool { return len(h.acks(1)) == 1 })
	h.dropLive(t)
	h.connected(t, 2)
	waitFor(t, 10*time.Second, "the second OnConnected", func() bool { return len(got.get()) == 3 })
	want := []string{
		"up 1 clock true hub held the snapshot and u1 true", "down true",
		"up 2 clock true hub held the snapshot and u1 true",
	}
	if g := got.get(); !slices.Equal(g, want) {
		t.Errorf("the lifecycle was %q, want %q", g, want)
	}
	for n := 1; n <= 2; n++ {
		fresh := fmt.Sprintf("fresh-%d", n)
		waitFor(t, 5*time.Second, "the ack on stream", func() bool { return len(h.acks(n)) == 1 })
		for _, id := range h.snapshots(n)[0].GetUnackedResults() {
			if id.GetValue() == fresh {
				t.Errorf("stream %d's snapshot lists %s, logged in OnConnected after it was sent", n, fresh)
			}
		}
		q := h.queued(n)
		if c := slices.Index(q, "result:"+fresh); c < 0 || slices.Contains(q[c+1:], "result:"+fresh) ||
			q[len(q)-1] != "ack:from-up" {
			t.Errorf("stream %d got %q, want %s once after the snapshot's results, then the ack", n, q, fresh)
		}
	}
}

// Hello advertises the hook's capabilities; a reserved, invalid or repeated
// one is dropped and logged, never sent.
func TestHelloCarriesTheAdvertisedCapabilities(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	capOf := func(name string, v uint32) *typev1.Capability { return &typev1.Capability{Name: name, Version: v} }
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.Capabilities = func() []*typev1.Capability {
			return []*typev1.Capability{
				capOf("tmux.observe", 1), capOf("exec.raw", 1), capOf("", 1), capOf("drain", 0),
				capOf("tmux.observe", 2), capOf("bad\nname", 1), capOf(strings.Repeat("x", 65), 1),
				nil, capOf("drain", 1), capOf("Bad", 1), capOf(strings.Repeat("y", 64), 1),
			}
		}
	})
	h.connected(t, 1)
	got := h.stream(1).hello.GetCapabilities()
	want := []*typev1.Capability{capOf("tmux.observe", 1), capOf("drain", 1), capOf(strings.Repeat("y", 64), 1)}
	if len(got) != len(want) {
		t.Fatalf("Hello.capabilities = %v, want %v", got, want)
	}
	for i := range want {
		if !proto.Equal(got[i], want[i]) {
			t.Errorf("capability %d = %v, want %v", i, got[i], want[i])
		}
	}
	var reasons []string
	for _, e := range a.events(t, "connect.capability_dropped") {
		r, _ := e["reason"].(string)
		reasons = append(reasons, r)
		if n, ok := e["name"].(string); ok && (n == "" || strings.ContainsAny(n, "\n") || len(n) > 64) {
			t.Errorf("a dropped name that is not safe to print was logged: %v", e)
		}
	}
	wantReasons := []string{"reserved", "invalid", "invalid", "repeat", "invalid", "invalid", "invalid", "invalid"}
	if !slices.Equal(reasons, wantReasons) {
		t.Errorf("drop reasons %q, want %q", reasons, wantReasons)
	}
	// The next Hello sends the same list and logs no drop again.
	h.dropLive(t)
	h.connected(t, 2)
	if got := h.stream(2).hello.GetCapabilities(); len(got) != len(want) {
		t.Errorf("the second Hello carries %v, want %v", got, want)
	}
	if n := len(a.events(t, "connect.capability_dropped")); n != len(wantReasons) {
		t.Errorf("%d drop lines after a reconnect, want %d: once per entry and run", n, len(wantReasons))
	}
}

// StatusChanged signals after the status changes, without blocking the
// loop when nobody reads it.
func TestStatusChangedSignalsAChange(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	ch := r.loop.StatusChanged()
	r.connected(t)
	select { // drain the signal of the connect, if it is still there
	case <-ch:
	default:
	}
	h.dropLive(t)
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no signal after the stream dropped")
	}
	h.connected(t, 2)
	r.connected(t)
}
