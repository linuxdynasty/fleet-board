package probe_test

import (
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing/probe"
)

func health(alive bool, failures uint32) *agentv1.ProbeHealth {
	return &agentv1.ProbeHealth{Alive: alive, ConsecutiveFailures: failures}
}

func TestStep(t *testing.T) {
	t.Parallel()
	online := hubv1.MachineState_MACHINE_STATE_ONLINE
	offline := hubv1.MachineState_MACHINE_STATE_OFFLINE
	for _, tc := range []struct {
		name   string
		cur    probe.State
		health *agentv1.ProbeHealth
		m      hubv1.MachineState
		failed bool
		want   probe.State
	}{
		{name: "requested turns alive on first health", cur: probe.Requested, health: health(true, 0), m: online, want: probe.Alive},
		{name: "requested fails on a failed start", cur: probe.Requested, health: health(false, 0), m: online, failed: true, want: probe.Failing},
		{name: "requested fails on one failure", cur: probe.Requested, health: health(false, 1), m: online, want: probe.Failing},
		{name: "alive fails on one failure", cur: probe.Alive, health: health(false, 1), m: online, want: probe.Failing},
		{name: "failing recovers on clean health", cur: probe.Failing, health: health(true, 0), m: online, want: probe.Alive},
		{name: "failing fails over on three", cur: probe.Failing, health: health(false, 3), m: online, want: probe.FailedOver},
		{name: "alive fails over on three", cur: probe.Alive, health: health(false, 3), m: online, want: probe.FailedOver},
		{name: "alive fails over when the machine is offline", cur: probe.Alive, health: health(true, 0), m: offline, want: probe.FailedOver},
		{name: "requested fails over when the machine is offline", cur: probe.Requested, health: health(true, 0), m: offline, want: probe.FailedOver},
		{name: "failed over is terminal", cur: probe.FailedOver, health: health(true, 0), m: online, want: probe.FailedOver},
		{name: "stopped is terminal", cur: probe.Stopped, health: health(true, 0), m: online, want: probe.Stopped},
		{name: "alive stays alive", cur: probe.Alive, health: health(true, 0), m: online, want: probe.Alive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probe.Step(tc.cur, tc.health, tc.m, tc.failed); got != tc.want {
				t.Fatalf("Step(%v) = %v, want %v", tc.cur, got, tc.want)
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()
	t.Run("a snapshot probe the hub dropped gets a stop", func(t *testing.T) {
		t.Parallel()
		got := probe.Reconcile(
			[]probe.SnapshotProbe{{ProbeID: "p1", Machine: "m1"}, {ProbeID: "p2", Machine: "m1"}},
			map[string]string{"p1": "m1"},
		)
		if len(got) != 1 || got[0] != (probe.StopPoint{ProbeID: "p2", Machine: "m1"}) {
			t.Fatalf("Reconcile() = %v, want one stop for p2 on m1", got)
		}
	})
	t.Run("a probe on the wrong machine gets a stop there", func(t *testing.T) {
		t.Parallel()
		got := probe.Reconcile(
			[]probe.SnapshotProbe{{ProbeID: "p1", Machine: "m2"}},
			map[string]string{"p1": "m1"},
		)
		if len(got) != 1 || got[0] != (probe.StopPoint{ProbeID: "p1", Machine: "m2"}) {
			t.Fatalf("Reconcile() = %v, want one stop for p1 on m2", got)
		}
	})
	t.Run("matching probes get nothing", func(t *testing.T) {
		t.Parallel()
		got := probe.Reconcile(
			[]probe.SnapshotProbe{{ProbeID: "p1", Machine: "m1"}},
			map[string]string{"p1": "m1"},
		)
		if len(got) != 0 {
			t.Fatalf("Reconcile() = %v, want no stops", got)
		}
	})
	t.Run("no snapshot probes get nothing", func(t *testing.T) {
		t.Parallel()
		if got := probe.Reconcile(nil, map[string]string{"p1": "m1"}); len(got) != 0 {
			t.Fatalf("Reconcile() = %v, want no stops", got)
		}
	})
}
