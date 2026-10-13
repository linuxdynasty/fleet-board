package probe_test

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing/probe"
)

var probeNow = time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)

func usageSupported() []*typev1.ParserSupport {
	return []*typev1.ParserSupport{{
		Feature:   typev1.ParserFeature_PARSER_FEATURE_USAGE,
		Supported: true,
	}}
}

func offer(signedIn, cap bool, support typev1.VersionSupport, parser []*typev1.ParserSupport) probe.HarnessOffer {
	return probe.HarnessOffer{
		Profile:        "",
		VersionSupport: support,
		ParserSupport:  parser,
		SignedIn:       signedIn,
		HasProbeCap:    cap,
	}
}

func goodOffer() probe.HarnessOffer {
	return offer(true, true, typev1.VersionSupport_VERSION_SUPPORT_SUPPORTED, nil)
}

func cand(id string, state hubv1.MachineState, labels []string, offers map[string][]probe.HarnessOffer) probe.Candidate {
	return probe.Candidate{
		ID:          id,
		State:       state,
		Labels:      labels,
		Harnesses:   offers,
		LastSuccess: time.Time{},
	}
}

func claudeAccount() probe.Account {
	return probe.Account{ID: "acc-1", Key: "key-1", Harness: "claude"}
}

func offerMap(harness string, o probe.HarnessOffer) map[string][]probe.HarnessOffer {
	return map[string][]probe.HarnessOffer{harness: {o}}
}

func TestGateRunNow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		last time.Time
		want bool
	}{
		{name: "never ran goes now", last: time.Time{}, want: true},
		{name: "a minute ago goes now", last: probeNow.Add(-time.Minute), want: true},
		{name: "thirty seconds ago waits", last: probeNow.Add(-30 * time.Second), want: false},
		{name: "fifty-nine seconds ago waits", last: probeNow.Add(-59 * time.Second), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probe.GateRunNow(probeNow, tc.last); got != tc.want {
				t.Fatalf("GateRunNow(%v) = %v, want %v", tc.last, got, tc.want)
			}
		})
	}
}

func TestPassiveGap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		passive float64
		latest  float64
		want    bool
	}{
		{name: "five points up triggers", passive: 55, latest: 50, want: true},
		{name: "five points down triggers", passive: 45, latest: 50, want: true},
		{name: "four points waits", passive: 54, latest: 50, want: false},
		{name: "same value waits", passive: 50, latest: 50, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probe.PassiveGap(tc.passive, tc.latest); got != tc.want {
				t.Fatalf("PassiveGap(%v, %v) = %v, want %v", tc.passive, tc.latest, got, tc.want)
			}
		})
	}
}

func TestSelectRunNow(t *testing.T) {
	t.Parallel()
	online := hubv1.MachineState_MACHINE_STATE_ONLINE
	active := &probe.Active{ProbeID: "p1", Machine: "m1", MachineState: online}
	t.Run("asked and allowed re-issues to the active probe", func(t *testing.T) {
		t.Parallel()
		got := probe.Select(probeNow, claudeAccount(), twoMachines(), active, probeNow.Add(-2*time.Minute), true)
		if got.Kind != probe.RunNow || got.Machine != "m1" {
			t.Fatalf("Select() = (%v, %q), want (RunNow, m1) (reason %q)", got.Kind, got.Machine, got.Reason)
		}
	})
	t.Run("asked but inside the minute stays steady", func(t *testing.T) {
		t.Parallel()
		got := probe.Select(probeNow, claudeAccount(), twoMachines(), active, probeNow.Add(-30*time.Second), true)
		if got.Kind != probe.None {
			t.Fatalf("Select() = (%v, %q), want None (reason %q)", got.Kind, got.Machine, got.Reason)
		}
	})
	t.Run("not asked stays steady", func(t *testing.T) {
		t.Parallel()
		got := probe.Select(probeNow, claudeAccount(), twoMachines(), active, time.Time{}, false)
		if got.Kind != probe.None {
			t.Fatalf("Select() = (%v, %q), want None (reason %q)", got.Kind, got.Machine, got.Reason)
		}
	})
	t.Run("asked with no probe and no candidate stays steady", func(t *testing.T) {
		t.Parallel()
		got := probe.Select(probeNow, claudeAccount(), nil, nil, time.Time{}, true)
		if got.Kind != probe.None {
			t.Fatalf("Select() = (%v, %q), want None (reason %q)", got.Kind, got.Machine, got.Reason)
		}
	})
}

func twoMachines() []probe.Candidate {
	online := hubv1.MachineState_MACHINE_STATE_ONLINE
	return []probe.Candidate{
		cand("m1", online, nil, offerMap("claude", goodOffer())),
		cand("m2", online, nil, offerMap("claude", goodOffer())),
	}
}

func TestSelectActive(t *testing.T) {
	t.Parallel()
	online := hubv1.MachineState_MACHINE_STATE_ONLINE
	offline := hubv1.MachineState_MACHINE_STATE_OFFLINE
	for _, tc := range []struct {
		name string
		m    []probe.Candidate
		in   *probe.Active
		// wantKind, wantMachine and wantStops describe the decision.
		wantKind    probe.Kind
		wantMachine string
		wantStops   []probe.StopPoint
	}{
		{
			name:        "a healthy active probe stays: two passes never assign twice",
			m:           twoMachines(),
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", MachineState: online},
			wantKind:    probe.None,
			wantMachine: "",
		},
		{
			name:        "one failure keeps the probe where it is",
			m:           twoMachines(),
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", Failures: 1, MachineState: online},
			wantKind:    probe.None,
			wantMachine: "",
		},
		{
			name:        "two failures still wait for the third",
			m:           twoMachines(),
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", Failures: 2, MachineState: online},
			wantKind:    probe.None,
			wantMachine: "",
		},
		{
			name:        "three failures fail over with a stop for the old machine",
			m:           twoMachines(),
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", Failures: 3, MachineState: online},
			wantKind:    probe.Failover,
			wantMachine: "m2",
			wantStops:   []probe.StopPoint{{ProbeID: "p1", Machine: "m1"}},
		},
		{
			name: "offline machine fails over at once",
			m: []probe.Candidate{
				cand("m1", offline, nil, offerMap("claude", goodOffer())),
				cand("m2", online, nil, offerMap("claude", goodOffer())),
			},
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", MachineState: offline},
			wantKind:    probe.Failover,
			wantMachine: "m2",
			wantStops:   []probe.StopPoint{{ProbeID: "p1", Machine: "m1"}},
		},
		{
			name: "stale machine never fails over",
			m: []probe.Candidate{
				cand("m1", hubv1.MachineState_MACHINE_STATE_STALE, nil, offerMap("claude", goodOffer())),
				cand("m2", online, nil, offerMap("claude", goodOffer())),
			},
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", MachineState: hubv1.MachineState_MACHINE_STATE_STALE},
			wantKind:    probe.None,
			wantMachine: "",
		},
		{
			name:        "failover with no other candidate stops the probe",
			m:           nil,
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", Failures: 3, MachineState: online},
			wantKind:    probe.Stop,
			wantMachine: "",
			wantStops:   []probe.StopPoint{{ProbeID: "p1", Machine: "m1"}},
		},
		{
			name:        "removed account stops its probe",
			m:           twoMachines(),
			in:          &probe.Active{ProbeID: "p1", Machine: "m1", MachineState: online},
			wantKind:    probe.Stop,
			wantMachine: "",
			wantStops:   []probe.StopPoint{{ProbeID: "p1", Machine: "m1"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			acct := claudeAccount()
			if tc.name == "removed account stops its probe" {
				acct.Gone = true
			}
			got := probe.Select(probeNow, acct, tc.m, tc.in, time.Time{}, false)
			if got.Kind != tc.wantKind || got.Machine != tc.wantMachine {
				t.Fatalf("Select() = (%v, %q), want (%v, %q) (reason %q)",
					got.Kind, got.Machine, tc.wantKind, tc.wantMachine, got.Reason)
			}
			if len(got.Stops) != len(tc.wantStops) {
				t.Fatalf("Select() stops = %v, want %v", got.Stops, tc.wantStops)
			}
			for i := range got.Stops {
				if got.Stops[i] != tc.wantStops[i] {
					t.Fatalf("Select() stops = %v, want %v", got.Stops, tc.wantStops)
				}
			}
		})
	}
}

func TestSelectPreference(t *testing.T) {
	t.Parallel()
	online := hubv1.MachineState_MACHINE_STATE_ONLINE
	old := probeNow.Add(-2 * time.Hour)
	recent := probeNow.Add(-time.Hour)
	for _, tc := range []struct {
		name string
		m    []probe.Candidate
		want string
	}{
		{
			name: "always-on laptop beats a plain desktop",
			m: []probe.Candidate{
				cand("desktop", online, nil, offerMap("claude", goodOffer())),
				cand("lap", online, []string{"always-on", "laptop"}, offerMap("claude", goodOffer())),
			},
			want: "lap",
		},
		{
			name: "desktop beats a laptop",
			m: []probe.Candidate{
				cand("lap", online, []string{"laptop"}, offerMap("claude", goodOffer())),
				cand("desk", online, nil, offerMap("claude", goodOffer())),
			},
			want: "desk",
		},
		{
			name: "hub machine beats another desktop",
			m: []probe.Candidate{
				func() probe.Candidate {
					c := cand("hub", online, nil, offerMap("claude", goodOffer()))
					c.IsHub = true
					return c
				}(),
				cand("desk", online, nil, offerMap("claude", goodOffer())),
			},
			want: "hub",
		},
		{
			name: "most recently successful wins the tie",
			m: []probe.Candidate{
				func() probe.Candidate {
					c := cand("old", online, nil, offerMap("claude", goodOffer()))
					c.LastSuccess = old
					return c
				}(),
				func() probe.Candidate {
					c := cand("new", online, nil, offerMap("claude", goodOffer()))
					c.LastSuccess = recent
					return c
				}(),
			},
			want: "new",
		},
		{
			name: "full order always-on desktop laptop hub recency",
			m: []probe.Candidate{
				cand("lap", online, []string{"laptop"}, offerMap("claude", goodOffer())),
				cand("desk", online, nil, offerMap("claude", goodOffer())),
				func() probe.Candidate {
					c := cand("hub", online, nil, offerMap("claude", goodOffer()))
					c.IsHub = true
					return c
				}(),
				cand("ao", online, []string{"always-on"}, offerMap("claude", goodOffer())),
			},
			want: "ao",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := probe.Select(probeNow, claudeAccount(), tc.m, nil, time.Time{}, false)
			if got.Kind != probe.Assign || got.Machine != tc.want {
				t.Fatalf("Select() = (%v, %q), want (Assign, %q) (reason %q)",
					got.Kind, got.Machine, tc.want, got.Reason)
			}
		})
	}
}

func TestSelectCandidates(t *testing.T) {
	t.Parallel()
	online := hubv1.MachineState_MACHINE_STATE_ONLINE
	for _, tc := range []struct {
		name string
		acct probe.Account
		m    probe.Candidate
		// wantMachine is empty when no assignment is expected.
		wantMachine string
	}{
		{
			name:        "online signed-in supported machine wins",
			acct:        claudeAccount(),
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {goodOffer()}}),
			wantMachine: "m1",
		},
		{
			name:        "stale machine is not a candidate",
			acct:        claudeAccount(),
			m:           cand("m1", hubv1.MachineState_MACHINE_STATE_STALE, nil, map[string][]probe.HarnessOffer{"claude": {goodOffer()}}),
			wantMachine: "",
		},
		{
			name:        "offline machine is not a candidate",
			acct:        claudeAccount(),
			m:           cand("m1", hubv1.MachineState_MACHINE_STATE_OFFLINE, nil, map[string][]probe.HarnessOffer{"claude": {goodOffer()}}),
			wantMachine: "",
		},
		{
			name: "draining machine is not a candidate",
			acct: claudeAccount(),
			m: func() probe.Candidate {
				c := cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {goodOffer()}})
				c.Draining = true
				return c
			}(),
			wantMachine: "",
		},
		{
			name:        "signed out with no owner mapping is not a candidate",
			acct:        claudeAccount(),
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {offer(false, true, typev1.VersionSupport_VERSION_SUPPORT_SUPPORTED, nil)}}),
			wantMachine: "",
		},
		{
			name: "owner codex mapping makes the machine a candidate without sign-in",
			acct: probe.Account{
				ID: "acc-2", Key: "key-2", Harness: "codex",
				Bindings: []probe.Binding{{Machine: "m1", Profile: ""}},
			},
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"codex": {offer(false, true, typev1.VersionSupport_VERSION_SUPPORT_SUPPORTED, nil)}}),
			wantMachine: "m1",
		},
		{
			name: "owner mapping for another machine does not qualify",
			acct: probe.Account{
				ID: "acc-2", Key: "key-2", Harness: "codex",
				Bindings: []probe.Binding{{Machine: "m9", Profile: ""}},
			},
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"codex": {offer(false, true, typev1.VersionSupport_VERSION_SUPPORT_SUPPORTED, nil)}}),
			wantMachine: "",
		},
		{
			name:        "unsupported harness version is not a candidate",
			acct:        claudeAccount(),
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {offer(true, true, typev1.VersionSupport_VERSION_SUPPORT_UNSUPPORTED, nil)}}),
			wantMachine: "",
		},
		{
			name: "unverified parsing without usage support is not a candidate",
			acct: claudeAccount(),
			m: cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {
				offer(true, true, typev1.VersionSupport_VERSION_SUPPORT_UNVERIFIED_PARSING, nil),
			}}),
			wantMachine: "",
		},
		{
			name: "unverified parsing with live usage parsing is a candidate",
			acct: claudeAccount(),
			m: cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {
				offer(true, true, typev1.VersionSupport_VERSION_SUPPORT_UNVERIFIED_PARSING,
					[]*typev1.ParserSupport{{
						Feature:     typev1.ParserFeature_PARSER_FEATURE_USAGE,
						ParsingLive: true,
					}}),
			}}),
			wantMachine: "m1",
		},
		{
			name: "unverified parsing with verified usage support is a candidate",
			acct: claudeAccount(),
			m: cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {
				offer(true, true, typev1.VersionSupport_VERSION_SUPPORT_UNVERIFIED_PARSING, usageSupported()),
			}}),
			wantMachine: "m1",
		},
		{
			name:        "missing probe capability is not a candidate",
			acct:        claudeAccount(),
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"claude": {offer(true, false, typev1.VersionSupport_VERSION_SUPPORT_SUPPORTED, nil)}}),
			wantMachine: "",
		},
		{
			name:        "no offer for the harness is not a candidate",
			acct:        claudeAccount(),
			m:           cand("m1", online, nil, map[string][]probe.HarnessOffer{"codex": {goodOffer()}}),
			wantMachine: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := probe.Select(probeNow, tc.acct, []probe.Candidate{tc.m}, nil, time.Time{}, false)
			if got.Machine != tc.wantMachine {
				t.Fatalf("Select() machine = %q, want %q (reason %q)", got.Machine, tc.wantMachine, got.Reason)
			}
		})
	}
}
