package probe

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// health emits this pass's ProbeHealth: the level the hub paces and
// fails over from. Every pass emits exactly one.
func (m *Manager) health(ctx context.Context, a *assignment, pane *typev1.PaneKey, now time.Time, alive bool, message string) error {
	_, err := m.em.Emit(ctx, &agentv1.Event{Body: &agentv1.Event_ProbeHealth{ProbeHealth: &agentv1.ProbeHealth{
		ProbeId:             a.probeID,
		AccountId:           a.accountID,
		AccountKey:          a.accountKey,
		Harness:             a.harness,
		Pane:                pane,
		Alive:               alive,
		LastSuccessTime:     a.lastSuccess,
		LastAttemptTime:     timestamppb.New(now),
		ConsecutiveFailures: a.consecutive,
		LastFailure:         a.lastFailure,
		Interval:            durationpb.New(a.interval),
		NextRunTime:         timestamppb.New(a.nextRun),
		Message:             message,
	}}})
	return err
}

// failed records a failed pass, emits its health, and builds its result.
// Backend text never reaches the message: fixed words only.
func (m *Manager) failed(ctx context.Context, a *assignment, pane *typev1.PaneKey, now time.Time, alive bool, code typev1.ReasonCode, retryable bool, message string) *agentv1.CommandResult {
	a.consecutive++
	a.lastFailure = code
	if err := m.health(ctx, a, pane, now, alive, message); err != nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: its health event did not store")
	}
	return fail(code, retryable, "probe: "+message)
}

// blocked records a startup-screen pass: alive=false, nothing typed, and
// health only, so the hub tries failover and raises its triage item. The
// result reason names the screen for the owner (FOLDER_UNTRUSTED for
// trust, 01 C3); health carries the pacing code (STARTUP_PROMPT_SEEN
// with the kind, 03b).
func (m *Manager) blocked(ctx context.Context, a *assignment, pane *typev1.PaneKey, now time.Time, result, health typev1.ReasonCode, kind, message string) *agentv1.CommandResult {
	a.consecutive++
	a.lastFailure = health
	if err := m.health(ctx, a, pane, now, false, message); err != nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: its health event did not store")
	}
	res := fail(result, true, "probe: "+message)
	if kind != "" {
		res.Metadata = map[string]string{"kind": kind}
	}
	return res
}

// succeeded records a pass that read a usage screen: failures reset, the
// reading goes out with the health, and the result stays OK.
func (m *Manager) succeeded(ctx context.Context, a *assignment, pane *typev1.PaneKey, now time.Time, reading *agentv1.UsageReading, message string) *agentv1.CommandResult {
	a.consecutive = 0
	a.lastFailure = typev1.ReasonCode_REASON_CODE_UNSPECIFIED
	a.lastSuccess = timestamppb.New(now)
	if _, err := m.em.Emit(ctx, &agentv1.Event{Body: &agentv1.Event_UsageReading{UsageReading: reading}}); err != nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: its reading event did not store")
	}
	if err := m.health(ctx, a, pane, now, true, message); err != nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: its health event did not store")
	}
	return ok("probe: " + message)
}
