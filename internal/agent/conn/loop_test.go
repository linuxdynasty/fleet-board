package conn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

func TestTheLoopSaysHelloThenSendsASnapshot(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)

	st, err := a.openDB(t).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	s := h.stream(1)
	hello := s.hello
	switch {
	case hello.GetMachineId().GetValue() != id.MachineID, hello.GetInstallId() != st.InstallID,
		hello.GetBootId() != "boot-1", hello.GetAgentVersion() != "test":
		t.Errorf("Hello does not name this agent: %v", hello)
	case hello.GetProtoMin() != protoversion.Min, hello.GetProtoMax() != protoversion.Max:
		t.Errorf("Hello proto range = %d..%d", hello.GetProtoMin(), hello.GetProtoMax())
	case hello.GetReason() != agentv1.ConnectReason_CONNECT_REASON_START, hello.GetLastEpoch() != 0,
		hello.GetLastEventSeq() != 0, hello.GetSentTime() == nil:
		t.Errorf("a first Hello = %v", hello)
	}
	snap := s.msgs[0].GetSnapshot()
	if snap == nil {
		t.Fatalf("the first message after Hello is %v, want the Snapshot", s.msgs[0])
	}
	if snap.GetSeq() < 1 || snap.GetSnapshotId() == "" || snap.GetTakenTime() == nil {
		t.Errorf("snapshot seq %d id %q taken %v", snap.GetSeq(), snap.GetSnapshotId(), snap.GetTakenTime())
	}
	if f := snap.GetFacts(); f.GetBootId() != "boot-1" || f.GetAgentVersion() != "test" || f.GetOs() == "" {
		t.Errorf("facts stub = %v", f)
	}
	if st.Epoch != 1 || st.Marks.SeqHWM < snap.GetSeq() {
		t.Errorf("agent.db epoch %d seq_hwm %d after the first HelloAck and snapshot seq %d",
			st.Epoch, st.Marks.SeqHWM, snap.GetSeq())
	}
	got := r.loop.Status()
	if got.State != conn.StateConnected || got.Peer != h.addr || got.ClockOffsetTime.IsZero() ||
		got.ClockOffset.Abs() > 5*time.Second {
		t.Errorf("status = %+v", got)
	}
	hellos := a.events(t, "connect.hello")
	if len(hellos) != 1 || hellos[0]["epoch"] != float64(1) || hellos[0]["reason"] != "start" ||
		hellos[0]["boot_id"] != "boot-1" {
		t.Errorf("connect.hello = %v", hellos)
	}
	r.running(t)
}

func TestTheLoopEndsOnlyWhenItMust(t *testing.T) {
	t.Parallel()
	byeAtHello := func(reason agentv1.ByeReason) func(*connectHub) {
		return func(h *connectHub) {
			h.onHello = func(int, *agentv1.Hello) *agentv1.Bye { return bye(reason) }
		}
	}
	cases := []struct {
		name string
		hub  func(*connectHub)
		// afterConnect runs once the first stream is connected.
		afterConnect func(t *testing.T, h *connectHub)
		opts         func(*conn.LoopOptions)
		want         conn.End
		hellos       int
	}{
		{name: "revoked at hello", hub: byeAtHello(agentv1.ByeReason_BYE_REASON_REVOKED), want: conn.EndRevoked, hellos: 1},
		{
			name: "unknown machine at hello", hub: byeAtHello(agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE),
			want: conn.EndUnknownMachine, hellos: 1,
		},
		{
			name: "revoked while connected", want: conn.EndRevoked, hellos: 1,
			afterConnect: func(t *testing.T, h *connectHub) {
				t.Helper()
				h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{
					Bye: bye(agentv1.ByeReason_BYE_REASON_REVOKED),
				}})
			},
		},
		{
			name: "another fleet", hub: func(h *connectHub) { h.fleetID = "fleet-other" },
			want: conn.EndFleetMismatch, hellos: 1,
		},
		{
			name: "certificate past its lifetime", want: conn.EndCertExpired, hellos: 0,
			opts: func(o *conn.LoopOptions) { o.Identity.CertNotAfter = time.Now().Add(-time.Minute) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			if tc.hub != nil {
				h.set(tc.hub)
			}
			r := startLoop(t, a, tc.opts)
			if tc.afterConnect != nil {
				h.connected(t, 1)
				tc.afterConnect(t, h)
			}
			err := r.result(t, 10*time.Second)
			var ended *conn.EndedError
			if !errors.As(err, &ended) || ended.End != tc.want {
				t.Fatalf("Run = %v, want the loop ended as %s", err, tc.want)
			}
			if got := r.loop.Status(); got.Ended != tc.want || got.State != conn.StateEnded {
				t.Errorf("status = %+v", got)
			}
			if n := h.count(); n != tc.hellos {
				t.Errorf("the hub saw %d Hellos, want %d", n, tc.hellos)
			}
			if ev := a.events(t, "connect.ended"); len(ev) != 1 || ev[0]["state"] != string(tc.want) {
				t.Errorf("connect.ended = %v", ev)
			}
			if tc.want == conn.EndFleetMismatch {
				msgs := h.stream(1).msgs
				if len(msgs) != 1 || msgs[0].GetBye().GetReason() != agentv1.ByeReason_BYE_REASON_FLEET_MISMATCH {
					t.Errorf("after another fleet's HelloAck the agent sent %v, want only Bye{FLEET_MISMATCH}", msgs)
				}
			}
		})
	}
}

func TestEverythingElseIsTriedAgain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		hub  func(*connectHub)
	}{
		{"hub restart", func(h *connectHub) {
			h.onHello = func(int, *agentv1.Hello) *agentv1.Bye { return bye(agentv1.ByeReason_BYE_REASON_HUB_RESTART) }
		}},
		{"proto unsupported", func(h *connectHub) {
			h.onHello = func(int, *agentv1.Hello) *agentv1.Bye {
				return bye(agentv1.ByeReason_BYE_REASON_PROTO_UNSUPPORTED)
			}
		}},
		{"duplicate identity", func(h *connectHub) {
			h.onHello = func(int, *agentv1.Hello) *agentv1.Bye {
				return bye(agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY)
			}
		}},
		{"a bye with no reason", func(h *connectHub) {
			h.onHello = func(int, *agentv1.Hello) *agentv1.Bye { return bye(agentv1.ByeReason_BYE_REASON_UNSPECIFIED) }
		}},
		{"an epoch not above the last", func(h *connectHub) {
			h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.Epoch = 0 }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			h.set(tc.hub)
			r := startLoop(t, a, nil)
			waitFor(t, 10*time.Second, "three tries", func() bool { return h.count() >= 3 })
			r.running(t)
			for n := 1; n <= 3; n++ {
				if len(h.snapshots(n)) != 0 {
					t.Errorf("stream %d got a snapshot", n)
				}
			}
			if got := h.stream(2).hello.GetReason(); got != agentv1.ConnectReason_CONNECT_REASON_RECONNECT {
				t.Errorf("the second Hello says %v, want RECONNECT", got)
			}
			r.cancel()
			if err := r.result(t, 10*time.Second); err != nil {
				t.Errorf("Run after cancel = %v, want nil", err)
			}
		})
	}
}

func TestAStopSaysByeToTheHub(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.cancel()
	if err := r.result(t, 10*time.Second); err != nil {
		t.Fatalf("Run after cancel = %v, want nil", err)
	}
	waitFor(t, 5*time.Second, "the stream to end", func() bool { return h.stream(1).ended })
	msgs := h.stream(1).msgs
	if last := msgs[len(msgs)-1]; last.GetBye().GetReason() != agentv1.ByeReason_BYE_REASON_SHUTDOWN {
		t.Errorf("the last message before a stop is %v, want Bye{SHUTDOWN}", last)
	}
	// The hub closed the stream in time, so nothing was cut, now or later.
	time.Sleep(2500 * time.Millisecond)
	if cut := a.events(t, "connect.shutdown_cut"); len(cut) != 0 {
		t.Errorf("connect.shutdown_cut = %v after a hub that closed in time, want none", cut)
	}
}
