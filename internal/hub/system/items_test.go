package system_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

var (
	warn  = hubv1.SystemState_SYSTEM_STATE_WARNING
	crit  = hubv1.SystemState_SYSTEM_STATE_CRITICAL
	ok    = hubv1.SystemState_SYSTEM_STATE_OK
	unk   = hubv1.SystemState_SYSTEM_STATE_UNKNOWN
	main  = hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_MAIN
	secs  = hubv1.SystemUnit_SYSTEM_UNIT_SECONDS
	above = hubv1.SystemDirection_SYSTEM_DIRECTION_ABOVE
)

func f(v float64) *float64 { return &v }

// agentMeasure is the hand-worked AGENT_CONNECTED MAIN measure: seconds,
// worse above, WARNING past 600, CRITICAL past 3,600, no holds.
func agentMeasure(value *float64) *hubv1.SystemMeasure {
	return &hubv1.SystemMeasure{
		Kind: main, Unit: secs, Direction: above,
		WarningThreshold: f(600), CriticalThreshold: f(3600), Value: value,
	}
}

// TestDecideRaise opens one item per signal at or above its 05b "Item
// raised at" level: WARNING signals raise at WARNING, the six
// CRITICAL-only signals stay silent at WARNING and raise at CRITICAL.
// The detail carries the full measure that set the state, with the
// reading behind it.
func TestDecideRaise(t *testing.T) {
	t.Parallel()
	agent := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED
	sync := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG
	disk := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE
	writer := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG
	queueFull := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL
	gib := float64(int64(1) << 30)
	cases := []struct {
		name string
		in   system.Verdict
		want system.Change
	}{
		{
			"agent warning raises warning",
			system.Verdict{
				Kind: agent, State: warn, SetBy: main,
				Machine: &typev1.MachineId{Value: "m1"}, Main: system.Known(900),
			},
			system.Change{
				Op: system.OpRaise, Fingerprint: "system:AGENT_CONNECTED:m1",
				Severity: hubv1.Severity_SEVERITY_WARNING, Title: "machine has gone quiet",
				Detail:  &hubv1.SystemDetail{Signal: agent, State: warn, Measure: agentMeasure(f(900))},
				Machine: &typev1.MachineId{Value: "m1"},
			},
		},
		{
			"sync warning raises nothing",
			system.Verdict{
				Kind: sync, State: warn, SetBy: main,
				Integration: &typev1.IntegrationId{Value: "github"}, Main: system.Known(400),
			},
			system.Change{},
		},
		{
			"disk critical raises critical hub-wide",
			system.Verdict{Kind: disk, State: crit, SetBy: main, Main: system.Known(gib)},
			system.Change{
				Op: system.OpRaise, Fingerprint: "system:DISK_FREE:hub",
				Severity: hubv1.Severity_SEVERITY_CRITICAL, Title: "disk space is critically low",
				Detail: &hubv1.SystemDetail{Signal: disk, State: crit, Measure: &hubv1.SystemMeasure{
					Kind: main, Unit: hubv1.SystemUnit_SYSTEM_UNIT_BYTES,
					Direction:        hubv1.SystemDirection_SYSTEM_DIRECTION_BELOW,
					WarningThreshold: f(10 * gib), CriticalThreshold: f(2 * gib),
					CriticalClearThreshold: f(3 * gib), Value: f(gib),
				}},
			},
		},
		{
			"writer critical by queue full raises critical with that measure",
			system.Verdict{
				Kind: writer, State: crit, SetBy: queueFull,
				Main: system.Known(0.5), QueueFull: system.Known(45),
			},
			system.Change{
				Op: system.OpRaise, Fingerprint: "system:WRITER_BATCH_LAG:hub",
				Severity: hubv1.Severity_SEVERITY_CRITICAL, Title: "hub writer is stalled",
				Detail: &hubv1.SystemDetail{Signal: writer, State: crit, Measure: &hubv1.SystemMeasure{
					Kind: queueFull, Unit: secs, Direction: above,
					CriticalThreshold: f(30), Value: f(45),
				}},
			},
		},
		{
			"sync critical raises action with the integration in the detail",
			system.Verdict{
				Kind: sync, State: crit, SetBy: main,
				Integration: &typev1.IntegrationId{Value: "github"}, Main: system.Known(700),
			},
			system.Change{
				Op: system.OpRaise, Fingerprint: "system:INTEGRATION_SYNC_LAG:github",
				Severity: hubv1.Severity_SEVERITY_ACTION, Title: "integration sync is lagging",
				Detail: &hubv1.SystemDetail{
					Signal: sync, State: crit, Measure: &hubv1.SystemMeasure{
						Kind: main, Unit: secs, Direction: above,
						WarningThreshold: f(300), CriticalThreshold: f(600), Value: f(700),
					},
					Integration: &typev1.IntegrationId{Value: "github"},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := system.Decide([]system.Verdict{tc.in}, nil)
			if tc.want.Op == 0 {
				if len(got) != 0 {
					t.Fatalf("Decide = %v, want no changes", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("Decide = %v, want one change", got)
			}
			checkChange(t, got[0], tc.want)
		})
	}
}

// TestDecideOpenItems keeps a flapping signal on one item: the same
// decision writes nothing, a worse state moves the severity in place, a
// CRITICAL-only signal falling back to WARNING moves its item down in
// place without opening a new one, a lost reading keeps the item
// silently, and only OK retires it. A new reading alone (the same signal,
// state and measure, a newer value) writes nothing: the item is a
// decision record, not live telemetry.
func TestDecideOpenItems(t *testing.T) {
	t.Parallel()
	agent := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED
	sync := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG
	warnDetail := &hubv1.SystemDetail{Signal: agent, State: warn, Measure: agentMeasure(f(900))}
	openWarn := system.OpenItem{
		Fingerprint: "system:AGENT_CONNECTED:m1",
		Severity:    hubv1.Severity_SEVERITY_WARNING, Detail: warnDetail,
	}
	warnVerdict := func() system.Verdict {
		return system.Verdict{
			Kind: agent, State: warn, SetBy: main,
			Machine: &typev1.MachineId{Value: "m1"}, Main: system.Known(900),
		}
	}
	syncWarn := func() system.Verdict {
		return system.Verdict{
			Kind: sync, State: warn, SetBy: main,
			Integration: &typev1.IntegrationId{Value: "github"}, Main: system.Known(400),
		}
	}
	cases := []struct {
		name string
		in   system.Verdict
		open []system.OpenItem
		want system.Change
	}{
		{
			"same warning writes nothing",
			warnVerdict(),
			[]system.OpenItem{openWarn},
			system.Change{},
		},
		{
			"escalation moves severity in place",
			system.Verdict{
				Kind: agent, State: crit, SetBy: main,
				Machine: &typev1.MachineId{Value: "m1"}, Main: system.Known(4000),
			},
			[]system.OpenItem{openWarn},
			system.Change{
				Op: system.OpUpdate, Fingerprint: "system:AGENT_CONNECTED:m1",
				Severity: hubv1.Severity_SEVERITY_ACTION, Title: "machine is offline",
				Detail:  &hubv1.SystemDetail{Signal: agent, State: crit, Measure: agentMeasure(f(4000))},
				Machine: &typev1.MachineId{Value: "m1"},
			},
		},
		{
			"moved-down warning at rest writes nothing",
			syncWarn(),
			[]system.OpenItem{{
				Fingerprint: "system:INTEGRATION_SYNC_LAG:github",
				Severity:    hubv1.Severity_SEVERITY_WARNING,
				Detail: &hubv1.SystemDetail{
					Signal: sync, State: warn, Measure: &hubv1.SystemMeasure{
						Kind: main, Unit: secs, Direction: above,
						WarningThreshold: f(300), CriticalThreshold: f(600), Value: f(400),
					},
					Integration: &typev1.IntegrationId{Value: "github"},
				},
			}},
			system.Change{},
		},
		{
			"critical-only fall to warning moves severity in place",
			system.Verdict{
				Kind: sync, State: warn, SetBy: main,
				Integration: &typev1.IntegrationId{Value: "github"}, Main: system.Known(400),
			},
			[]system.OpenItem{{
				Fingerprint: "system:INTEGRATION_SYNC_LAG:github",
				Severity:    hubv1.Severity_SEVERITY_ACTION,
				Detail: &hubv1.SystemDetail{
					Signal: sync, State: crit, Measure: &hubv1.SystemMeasure{
						Kind: main, Unit: secs, Direction: above,
						WarningThreshold: f(300), CriticalThreshold: f(600), Value: f(700),
					},
					Integration: &typev1.IntegrationId{Value: "github"},
				},
			}},
			system.Change{
				Op: system.OpUpdate, Fingerprint: "system:INTEGRATION_SYNC_LAG:github",
				Severity: hubv1.Severity_SEVERITY_WARNING, Title: "integration sync is still lagging",
				Detail: &hubv1.SystemDetail{
					Signal: sync, State: warn, Measure: &hubv1.SystemMeasure{
						Kind: main, Unit: secs, Direction: above,
						WarningThreshold: f(300), CriticalThreshold: f(600), Value: f(400),
					},
					Integration: &typev1.IntegrationId{Value: "github"},
				},
			},
		},
		{
			"new reading alone writes nothing",
			system.Verdict{
				Kind: agent, State: warn, SetBy: main,
				Machine: &typev1.MachineId{Value: "m1"}, Main: system.Known(915),
			},
			[]system.OpenItem{openWarn},
			system.Change{},
		},
		{
			"unknown keeps its item",
			system.Verdict{
				Kind: agent, State: unk,
				Machine: &typev1.MachineId{Value: "m1"},
			},
			[]system.OpenItem{openWarn},
			system.Change{},
		},
		{
			"ok retires its item",
			system.Verdict{
				Kind: agent, State: ok,
				Machine: &typev1.MachineId{Value: "m1"}, Main: system.Known(5),
			},
			[]system.OpenItem{openWarn},
			system.Change{Op: system.OpRetire, Fingerprint: "system:AGENT_CONNECTED:m1"},
		},
		{
			"ok with no item writes nothing",
			system.Verdict{
				Kind: agent, State: ok,
				Machine: &typev1.MachineId{Value: "m1"}, Main: system.Known(5),
			},
			nil,
			system.Change{},
		},
		{
			"unknown with no item writes nothing",
			system.Verdict{
				Kind: agent, State: unk,
				Machine: &typev1.MachineId{Value: "m1"},
			},
			nil,
			system.Change{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := system.Decide([]system.Verdict{tc.in}, tc.open)
			if tc.want.Op == 0 {
				if len(got) != 0 {
					t.Fatalf("Decide = %v, want no changes", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("Decide = %v, want one change", got)
			}
			checkChange(t, got[0], tc.want)
		})
	}
}

// checkChange compares one decided write with the hand-worked want: the op,
// the dedupe key, the severity, the exact title, the detail and the
// machine subject.
func checkChange(t *testing.T, got, want system.Change) {
	t.Helper()
	if got.Op != want.Op || got.Fingerprint != want.Fingerprint || got.Severity != want.Severity {
		t.Fatalf("change = op %v key %q severity %v, want op %v key %q severity %v",
			got.Op, got.Fingerprint, got.Severity, want.Op, want.Fingerprint, want.Severity)
	}
	if want.Op == system.OpRetire {
		return
	}
	if got.Title != want.Title {
		t.Fatalf("title = %q, want %q", got.Title, want.Title)
	}
	if !proto.Equal(got.Detail, want.Detail) {
		t.Fatalf("detail = %v, want %v", got.Detail, want.Detail)
	}
	if (got.Machine == nil) != (want.Machine == nil) ||
		(got.Machine != nil && got.Machine.GetValue() != want.Machine.GetValue()) {
		t.Fatalf("machine = %v, want %v", got.Machine, want.Machine)
	}
}
