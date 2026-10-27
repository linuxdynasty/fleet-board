package api

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Dispatch timeouts (02j "Timing", defaults.md): the hub never invents
// them, and the agent takes an unset deadline as already past, so every
// dispatch command carries them. Row 23's scheduler passes these through;
// a fleet.yaml override never reaches the wire.
const (
	dispatchReadyTimeout      = 90 * time.Second
	dispatchGoalFileTimeout   = 30 * time.Second
	dispatchDefaultMaxSyncLag = 10 * time.Minute
)

func readyTimeout(f *Fleet) *durationpb.Duration {
	return durationpb.New(dispatchReadyTimeout)
}

func goalFileTimeout(f *Fleet) *durationpb.Duration {
	return durationpb.New(dispatchGoalFileTimeout)
}

// maxSyncLag is how far behind the provider the mirror may be before
// claims count as stale (02j timing.max_sync_lag, default 10 min).
func (f *Fleet) maxSyncLag() time.Duration {
	if f.maxLag > 0 {
		return f.maxLag
	}
	return dispatchDefaultMaxSyncLag
}

// agentSpec builds the agent's GoalSpec from a decided dispatch (03c
// "Consent form"): the consent list rides as acts, never text — the
// adapter renders and joins them, so nothing the hub sends can add a
// `--`, a comma or a newline to the typed line. worktree_mode AUTO is
// AGENT_ENSURES in row 22: the driver's checkout detection needs the
// machine inventory row 23 owns. session_id is hub-minted for the new
// window (or ignored on the reuse path); account_key stays unset until
// the identity probe maps AccountKey to AccountId.
func (fx *dispatchFacts) agentSpec(goalID string) *agentv1.GoalSpec {
	spec := &agentv1.GoalSpec{
		GoalId:       &typev1.GoalId{Value: goalID},
		Workdir:      &typev1.Workdir{Repo: &typev1.RepoRef{Host: fx.repoHost, Path: fx.repoPath}},
		Harness:      &typev1.Harness{Name: fx.harness},
		AccountId:    &typev1.AccountId{Value: fx.accountID},
		Consent:      fx.consent,
		GoalText:     fx.goalText,
		WorktreeMode: agentv1.WorktreeMode_WORKTREE_MODE_AGENT_ENSURES,
		Branch:       fx.branch,
		WorkItem:     fx.workItemRef,
		Repo:         &typev1.RepoRef{Host: fx.repoHost, Path: fx.repoPath},
		Model:        fx.model,
		Effort:       fx.effort,
		FreshContext: fx.fresh,
	}
	if fx.sessionID != "" {
		spec.SessionId = &typev1.SessionId{Value: fx.sessionID}
	}
	if fx.changeRequest != nil {
		spec.ChangeRequest = fx.changeRequest
	}
	if fx.profile != "" {
		spec.Profile = fx.profile
	}
	if fx.dispatchReason != "" {
		spec.DispatchReason = fx.dispatchReason
	}
	return spec
}

// countDispatch counts a dispatch RPC by mode and result
// (fleet_hub_dispatch_total, 03c "Telemetry").
func (f *Fleet) countDispatch(ctx context.Context, mode, result string) {
	f.dispatches.Add(ctx, 1, metric.WithAttributes(
		attribute.String("mode", mode), attribute.String("result", result)))
}

// countGoalMove counts a recorded goal transition by its endpoints
// (fleet_hub_goal_transitions_total, 03l "Telemetry"). Phases and
// reasons are closed contract enums, so the labels stay bounded.
func (f *Fleet) countGoalMove(ctx context.Context, from, to, reason string) {
	f.goalMoves.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from", from), attribute.String("to", to),
		attribute.String("reason", reason)))
}

// fillSpecFacts assembles the GoalSpec fields that only the assembler
// knows: the repo split, the text, the model and effort, the work item
// and change request refs (keys wrapped with the display the window and
// branch templates render), the profile and the rendered reason.
func (fx *dispatchFacts) fillSpecFacts(in *hubv1.GoalSpecInput) {
	fx.repoHost = in.GetRepo().GetHost()
	fx.repoPath = in.GetRepo().GetPath()
	fx.goalText = in.GetGoalText()
	fx.model = in.GetModel()
	fx.effort = in.GetEffort()
	fx.fresh = in.GetFreshContext()
	fx.profile = in.GetPlacement().GetProfile()
	fx.workItemRef = &typev1.WorkItemRef{Key: in.GetWorkItem(), Display: fx.display}
	if cr := in.GetChangeRequest(); cr != nil {
		fx.changeRequest = &typev1.WorkItemRef{Key: cr}
	}
	if r := in.GetDispatchReason(); r != "" {
		fx.dispatchReason = r
	}
}
