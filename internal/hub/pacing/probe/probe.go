package probe

import (
	"sort"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Tuning from 03b and defaults.md.
const (
	// FailoverFailures is the consecutive-failure count that moves a probe
	// off its machine (03b, "Failover").
	FailoverFailures = 3
	// RunNowInterval is the closest two run_now triggers may run on one
	// account (03b, "Event-triggered probes"; defaults.md).
	RunNowInterval = time.Minute
	// PassiveTriggerPoints is how far a passive reading must sit from the
	// latest probe reading to trigger a probe run (03b; defaults.md).
	PassiveTriggerPoints = 5.0
)

// LabelAlwaysOn marks a machine the owner keeps awake; LabelLaptop marks a
// machine that sleeps (03b, "Probe assignment").
const (
	LabelAlwaysOn = "always-on"
	LabelLaptop   = "laptop"
)

// Binding is one owner-mapped (machine, profile) for an account the harness
// shows no identity for (Codex; 03b, "Codex mapping").
type Binding struct {
	Machine string
	Profile string
}

// Account is the slice of an account the assignment decision reads.
type Account struct {
	ID      string
	Key     string
	Harness string
	// Bindings maps a Codex account onto machines (03b, "Codex mapping").
	Bindings []Binding
	// Gone stops the probe: the account was removed (03b state machine).
	Gone bool
}

// HarnessOffer is one harness on one machine as the scheduler sees it: the
// snapshot's signed-in row, the version support and the advertised
// usage.probe.<harness> capability folded in by the caller.
type HarnessOffer struct {
	Profile        string
	VersionSupport typev1.VersionSupport
	ParserSupport  []*typev1.ParserSupport
	// SignedIn reports the snapshot's accounts[] row for the account key.
	SignedIn bool
	// HasProbeCap reports the advertised usage.probe.<harness> capability.
	HasProbeCap bool
}

// Candidate is one machine as the scheduler sees it for probe assignment.
type Candidate struct {
	ID     string
	State  hubv1.MachineState
	Labels []string
	// Draining machines take no new probes (03b, "Probe assignment").
	Draining bool
	// IsHub marks the machine the hub itself runs on (preference).
	IsHub bool
	// Harnesses offers by harness name.
	Harnesses map[string][]HarnessOffer
	// LastSuccess is the most recent probe success on this machine; the
	// last preference tie-break (03b, "Probe assignment").
	LastSuccess time.Time
}

// Active is the account's current probe, if any.
type Active struct {
	ProbeID string
	Machine string
	// Failures is the consecutive-failure count from ProbeHealth.
	Failures uint32
	// MachineState is the current state of Machine.
	MachineState hubv1.MachineState
}

// Kind is what the scheduler should do after a pass.
type Kind int

const (
	// None keeps everything as it is.
	None Kind = iota
	// Assign sends EnsureProbe{run_now=true} to Machine for a new probe.
	Assign
	// Failover stops Stops and assigns Machine (StopProbe to the old
	// machine, EnsureProbe to the new one).
	Failover
	// RunNow re-issues EnsureProbe{run_now=true} to the active probe.
	RunNow
	// Stop tears the active probe down with no replacement.
	Stop
)

// StopPoint is one probe session to tear down with StopProbe.
type StopPoint struct {
	ProbeID string
	Machine string
}

// Decision is one pass over one account. Assign carries run_now (a fresh
// command_id runs once); RunNow passed GateRunNow.
type Decision struct {
	Kind    Kind
	Machine string
	Profile string
	Stops   []StopPoint
	Reason  string
}

// Select runs one scheduler pass for one account: filter the candidates by
// the 03b rules, rank them by the 03b preference, and assign, fail over, run
// now, or leave the probe alone. Two passes never assign one account twice:
// an account with an active probe gets no new assignment, only failover or
// run_now.
func Select(now time.Time, account Account, cands []Candidate, active *Active, lastRunNow time.Time, runNowAsked bool) Decision {
	if account.Gone {
		if active != nil {
			return Decision{
				Kind:   Stop,
				Stops:  []StopPoint{{ProbeID: active.ProbeID, Machine: active.Machine}},
				Reason: "stop:account_removed",
			}
		}
		return Decision{Kind: None, Reason: "none:account_removed"}
	}
	type ranked struct {
		c Candidate
		o HarnessOffer
	}
	var eligible []ranked
	for _, c := range cands {
		if c.State != hubv1.MachineState_MACHINE_STATE_ONLINE || c.Draining {
			continue
		}
		for _, o := range c.Harnesses[account.Harness] {
			if !o.HasProbeCap || !usageOK(o) {
				continue
			}
			if !o.SignedIn && !bound(account.Bindings, c.ID, o.Profile) {
				continue
			}
			eligible = append(eligible, ranked{c, o})
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return less(eligible[i].c, eligible[i].o, eligible[j].c, eligible[j].o)
	})
	if active == nil {
		if len(eligible) == 0 {
			return Decision{Kind: None, Reason: "none:no_candidate"}
		}
		return Decision{
			Kind: Assign, Machine: eligible[0].c.ID, Profile: eligible[0].o.Profile,
			Reason: "assign:" + eligible[0].c.ID,
		}
	}
	if failoverNeeded(active) {
		stops := []StopPoint{{ProbeID: active.ProbeID, Machine: active.Machine}}
		// A machine that just failed three runs is not picked again at
		// once; the next pass reconsiders it (03b, "Failover").
		var rest []ranked
		for _, e := range eligible {
			if active.Failures >= FailoverFailures && e.c.ID == active.Machine {
				continue
			}
			rest = append(rest, e)
		}
		if len(rest) == 0 {
			return Decision{Kind: Stop, Stops: stops, Reason: "stop:no_candidate"}
		}
		return Decision{
			Kind: Failover, Machine: rest[0].c.ID, Profile: rest[0].o.Profile,
			Stops: stops, Reason: "failover:" + rest[0].c.ID,
		}
	}
	// Failover wins over a re-issue: the fresh assignment carries run_now.
	if runNowAsked {
		if GateRunNow(now, lastRunNow) {
			return Decision{Kind: RunNow, Machine: active.Machine, Reason: "run_now:" + active.Machine}
		}
		return Decision{Kind: None, Reason: "none:run_now_limited"}
	}
	return Decision{Kind: None, Reason: "none:steady"}
}

// usageOK applies the version rule: SUPPORTED, or UNVERIFIED_PARSING with
// the USAGE parser verified or reading live screens (03b).
func usageOK(o HarnessOffer) bool {
	switch o.VersionSupport {
	case typev1.VersionSupport_VERSION_SUPPORT_SUPPORTED:
		return true
	case typev1.VersionSupport_VERSION_SUPPORT_UNVERIFIED_PARSING:
		for _, p := range o.ParserSupport {
			if p.GetFeature() == typev1.ParserFeature_PARSER_FEATURE_USAGE &&
				(p.GetSupported() || p.GetParsingLive()) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// failoverNeeded moves the probe when its machine is OFFLINE (never STALE:
// a sleeping laptop flaps through STALE) or its health failed three runs in
// a row (03b, "Failover").
func failoverNeeded(active *Active) bool {
	return active.MachineState == hubv1.MachineState_MACHINE_STATE_OFFLINE ||
		active.Failures >= FailoverFailures
}

// SnapshotProbe is one probe session an agent's snapshot reports.
type SnapshotProbe struct {
	ProbeID string
	Machine string
}

// Reconcile is the snapshot safety net (03b, "Failover"; W1, "Snapshot
// reconcile"): a probe in Snapshot.probes[] that the hub no longer assigns
// to that machine gets a fresh StopProbe there. assigned maps probe id to
// the machine the hub assigns it to.
func Reconcile(snapshot []SnapshotProbe, assigned map[string]string) []StopPoint {
	var stops []StopPoint
	for _, s := range snapshot {
		if assigned[s.ProbeID] != s.Machine {
			stops = append(stops, StopPoint(s))
		}
	}
	return stops
}

// GateRunNow allows at most one run_now per account per minute (03b,
// "Event-triggered probes"). A zero last run never blocks.
func GateRunNow(now, last time.Time) bool {
	return last.IsZero() || !now.Before(last.Add(RunNowInterval))
}

// PassiveGap reports whether a passive reading jumped at least five points
// off the latest probe reading and should trigger a probe run (03b,
// sequence diagram; defaults.md). A passive reading never paces alone.
func PassiveGap(passive, latestProbe float64) bool {
	gap := passive - latestProbe
	if gap < 0 {
		gap = -gap
	}
	return gap >= PassiveTriggerPoints
}

// bound reports an owner Codex mapping for the machine and profile.
func bound(bindings []Binding, machine, profile string) bool {
	for _, b := range bindings {
		if b.Machine == machine && b.Profile == profile {
			return true
		}
	}
	return false
}

// less ranks candidates by the 03b preference: always-on first, then not a
// laptop, then the hub machine, then the most recently successful. The
// machine id breaks the remaining ties so a pass is stable.
func less(ac Candidate, ao HarnessOffer, bc Candidate, bo HarnessOffer) bool {
	aa, bb := hasLabel(ac.Labels, LabelAlwaysOn), hasLabel(bc.Labels, LabelAlwaysOn)
	if aa != bb {
		return aa
	}
	al, bl := hasLabel(ac.Labels, LabelLaptop), hasLabel(bc.Labels, LabelLaptop)
	if al != bl {
		return !al
	}
	if ac.IsHub != bc.IsHub {
		return ac.IsHub
	}
	if !ac.LastSuccess.Equal(bc.LastSuccess) {
		return ac.LastSuccess.After(bc.LastSuccess)
	}
	if ac.ID != bc.ID {
		return ac.ID < bc.ID
	}
	return ao.Profile < bo.Profile
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
