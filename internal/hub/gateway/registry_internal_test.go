package gateway

import (
	"context"
	"io"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// endedWith reports how s ended, or false while it has not.
func endedWith(s *stream) (agentv1.ByeReason, statedb.EndReason, bool) {
	select {
	case <-s.ended:
		return s.bye, s.reason, true
	default:
		return 0, "", false
	}
}

// The higher epoch wins whatever order the two Hello results arrive in,
// and only a stream the registry held is displaced.
func TestRegistryHigherEpochWins(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	five, six := newStream("m1", "boot-a", "p1", 5), newStream("m1", "boot-b", "p2", 6)
	if got, old := r.register(six); got != registeredLive || old != nil {
		t.Fatalf("register(6) = %v, %v; want live, no old", got, old)
	}
	// Epoch 5's result arrives late: it lost, and the winner is untouched.
	if got, old := r.register(five); got != registeredLost || old != six {
		t.Fatalf("register(5) after 6 = %v, %v; want lost to 6", got, old)
	}
	if _, _, ended := endedWith(six); ended || !r.isLive(six) || r.isLive(five) {
		t.Fatal("the late lower epoch disturbed the live stream")
	}
	// A newer one displaces 6 with Bye{DUPLICATE_IDENTITY}.
	seven := newStream("m1", "boot-c", "p3", 7)
	if got, old := r.register(seven); got != registeredLive || old != six {
		t.Fatalf("register(7) = %v, %v; want live replacing 6", got, old)
	}
	if bye, reason, ended := endedWith(six); !ended || bye != agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY ||
		reason != statedb.EndDisplaced {
		t.Errorf("6 ended %v with %v %v; want DUPLICATE_IDENTITY, displaced", ended, bye, reason)
	}
	// The displaced stream's late close does not remove the winner.
	r.remove(six)
	if !r.isLive(seven) {
		t.Error("removing the displaced stream removed the live one")
	}
	if got := r.live("m1"); got.Epoch != 7 {
		t.Errorf("live = %+v, want epoch 7", got)
	}
}

// live copies the last Heartbeat time; cancel ends the live stream with
// the given Bye; byeAll ends every stream with HUB_RESTART and refuses
// later registrations.
func TestRegistryLiveCancelAndByeAll(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if got := r.live("m1"); got.Epoch != 0 {
		t.Fatalf("live of an unknown machine = %+v", got)
	}
	a, b := newStream("m1", "boot", "p", 1), newStream("m2", "boot", "p", 1)
	r.register(a)
	r.register(b)
	at := time.Unix(1_700_000_000, 0)
	a.beat.Store(at.UnixNano())
	if got := r.live("m1"); got.Epoch != 1 || !got.LastHeartbeat.Equal(at) {
		t.Errorf("live = %+v, want epoch 1 with the heartbeat", got)
	}
	if r.cancel("m3", agentv1.ByeReason_BYE_REASON_REVOKED, statedb.EndRevoked) {
		t.Error("cancel of a machine with no stream reported true")
	}
	if !r.cancel("m1", agentv1.ByeReason_BYE_REASON_REVOKED, statedb.EndRevoked) {
		t.Fatal("cancel of a live stream reported false")
	}
	if bye, reason, _ := endedWith(a); bye != agentv1.ByeReason_BYE_REASON_REVOKED || reason != statedb.EndRevoked {
		t.Errorf("cancelled stream ended with %v %v", bye, reason)
	}
	r.byeAll()
	if bye, reason, ended := endedWith(b); !ended || bye != agentv1.ByeReason_BYE_REASON_HUB_RESTART || reason != statedb.EndHubRestart {
		t.Errorf("byeAll ended b %v with %v %v", ended, bye, reason)
	}
	if got, _ := r.register(newStream("m4", "boot", "p", 1)); got != registeredClosing {
		t.Errorf("register after byeAll = %v, want closing", got)
	}
}

// The first end wins and send never blocks.
func TestStreamEndOnceAndSendNeverBlocks(t *testing.T) {
	t.Parallel()
	s := newStream("m1", "boot", "p", 1)
	for range outQueue {
		if !s.send(&agentv1.ConnectResponse{}) {
			t.Fatal("send refused below the queue size")
		}
	}
	if s.send(&agentv1.ConnectResponse{}) {
		t.Error("send past the queue size reported true")
	}
	s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndAgentBye)
	s.end(agentv1.ByeReason_BYE_REASON_HUB_RESTART, statedb.EndHubRestart)
	if s.endReason() != statedb.EndAgentBye || s.bye != agentv1.ByeReason_BYE_REASON_UNSPECIFIED {
		t.Errorf("ended %v %v, want the first end", s.bye, s.reason)
	}
}

// One Hello per machine per HelloEvery; a refusal does not move the
// window; a clock stepped back allows the Hello.
func TestHelloLimit(t *testing.T) {
	t.Parallel()
	l := newHelloLimit()
	t0 := time.Unix(1_700_000_000, 0)
	steps := []struct {
		machine string
		at      time.Duration
		ok      bool
		wait    time.Duration
	}{
		{"m1", 0, true, 0},
		{"m2", time.Second, true, 0},
		{"m1", 4 * time.Second, false, time.Second},
		{"m1", 4*time.Second + 900*time.Millisecond, false, 100 * time.Millisecond},
		{"m1", 5 * time.Second, true, 0},
		{"m1", time.Second, true, 0}, // the clock stepped back
	}
	for i, st := range steps {
		ok, wait := l.allow(st.machine, t0.Add(st.at))
		if ok != st.ok || wait != st.wait {
			t.Errorf("step %d: allow(%s, +%v) = %v, %v; want %v, %v", i, st.machine, st.at, ok, wait, st.ok, st.wait)
		}
	}
}

// A registered arm handler gets its messages; the rest are counted as
// unhandled.
func TestArmTableRunsRegisteredHandlers(t *testing.T) {
	t.Parallel()
	var got []string
	tbl := armTable{}
	tbl.add("settings_ack", func(_ context.Context, _ *agentService, s *stream, m *agentv1.ConnectRequest) {
		got = append(got, s.machineID+":"+armKind(m))
	})
	f := newTestDeps(t)
	f.arms = tbl
	s := newStream("m1", "boot", "p", 1)
	seen := map[string]bool{}
	f.handle(context.Background(), s, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_SettingsAck{}}, seen)
	f.handle(context.Background(), s, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{}}, seen)
	if len(got) != 1 || got[0] != "m1:settings_ack" || !seen["event"] || seen["settings_ack"] {
		t.Errorf("handled %v, unhandled seen %v", got, seen)
	}
}

// newTestDeps is an agentService with Connect's dependencies and no store
// or listener.
func newTestDeps(t *testing.T) *agentService {
	t.Helper()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	cd, err := newConnectDeps(ServerOptions{Telemetry: tel})
	if err != nil {
		t.Fatal(err)
	}
	return &agentService{enrollDeps: &enrollDeps{log: tel.Logger, now: time.Now}, connectDeps: cd}
}
