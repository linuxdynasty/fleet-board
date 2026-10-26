package startgoal

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"google.golang.org/protobuf/types/known/durationpb"
)

// lowDiskBytes is the agent low-disk point (defaults.md, ADR 0024):
// DISK_LOW under 5 GiB free on the state volume.
const lowDiskBytes = 5 * 1024 * 1024 * 1024

// defaultReadyTimeout and defaultFileTimeout are the 02j decided
// defaults (defaults.md) when the command leaves them unset.
const (
	defaultReadyTimeout = 90 * time.Second
	defaultFileTimeout  = 30 * time.Second
)

func errSeam(name string) error {
	return fmt.Errorf("startgoal: %s seam is required", name)
}

// Report receives one step start (failed false) or one step failure
// (failed true, ending the composite). The handler adapts it to
// CommandProgress; tests record it.
type Report func(step string, index int, failed bool, reason typev1.ReasonCode, msg string)

// state carries one run across its steps.
type state struct {
	cmd     *agentv1.StartGoal
	spec    *agentv1.GoalSpec
	goal    string
	harness string
	rearm   bool
	reuse   bool
	mode    agentv1.WorktreeMode

	pane     *typev1.PaneKey
	session  *agentv1.TmuxSession
	reused   bool
	worktree string
	branch   string
	head     string

	repoShipsOwn bool
	typedLine    string

	started      time.Time
	readyTimeout time.Duration
	fileTimeout  time.Duration
}

// Run executes the 14 steps in 02h order, reporting each start and the
// one failure. A nil Report reports nothing (tests pass one).
func (r *Runner) Run(ctx context.Context, cmd *agentv1.StartGoal, rep Report) *agentv1.CommandResult {
	st, res := r.open(cmd)
	if res != nil {
		return res
	}
	return r.from(ctx, st, 1, rep)
}

// Resume continues a restarted run at the persisted step (exec.PerStep:
// the index of the last started step, 0 when none started). Steps 11
// and 13 are never retyped: after a crash they end UNKNOWN_OUTCOME,
// unless step 14's proof already shows this goal's id, which turns
// type_goal into OK and continues at confirm_goal_file.
func (r *Runner) Resume(ctx context.Context, cmd *agentv1.StartGoal, from int, rep Report) *agentv1.CommandResult {
	st, res := r.open(cmd)
	if res != nil {
		return res
	}
	if from <= 0 {
		return r.from(ctx, st, 1, rep)
	}
	name := ""
	if from >= 1 && from <= len(steps) {
		name = steps[from-1]
	}
	// Best-effort location: the pane and worktree the earlier attempt
	// used. Nothing is typed or created here; when nothing is found the
	// proof below fails closed.
	r.locate(ctx, st)
	if nonIdempotent(name) {
		if name == "type_goal" && st.worktree != "" && r.proven(ctx, st) {
			return r.from(ctx, st, stepIndex("confirm_goal_file"), rep)
		}
		failedStep := name
		if failedStep == "" {
			failedStep = "type_goal"
		}
		return r.fail(failedStep, typev1.Status_STATUS_UNKNOWN_OUTCOME,
			typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
			"the agent restarted mid-step and cannot tell whether the command landed", rep)
	}
	if err := r.rederive(ctx, st, from); err != nil {
		return r.fail(stepName(from), typev1.Status_STATUS_FAILED,
			typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil, err.Error(), rep)
	}
	return r.from(ctx, st, from, rep)
}

func stepName(index int) string {
	if index >= 1 && index <= len(steps) {
		return steps[index-1]
	}
	return "preflight"
}

// open validates the command shape (nothing typed yet) and derives the
// static run state.
func (r *Runner) open(cmd *agentv1.StartGoal) (*state, *agentv1.CommandResult) {
	if cmd == nil || cmd.GetSpec() == nil {
		return nil, failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil, "startgoal: StartGoal needs a spec")
	}
	spec := cmd.GetSpec()
	goal := spec.GetGoalId().GetValue()
	if !goalfile.ValidGoalID(goal) {
		return nil, failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil, "startgoal: goal id must match the 02e id format")
	}
	harness := spec.GetHarness().GetName()
	if !goalHarness(harness) {
		return nil, failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil,
			fmt.Sprintf("startgoal: harness %q has no native /goal", harness))
	}
	mode := spec.GetWorktreeMode()
	if mode == agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH && len(spec.GetConsent()) > 0 {
		return nil, failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil,
			"startgoal: a scratch goal carries no consent")
	}
	if cmd.GetRearmReason() != "" && cmd.GetReusePane() == nil {
		return nil, failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil,
			"startgoal: re-arm names the goal's own pane")
	}
	if cmd.GetRearmReason() != "" {
		// Re-arm runs on the goal's own pane only, checked before step
		// 1 even with skip_preflight; any other pane is
		// SESSION_NOT_AVAILABLE, and the pane counts as free because the
		// live goal is its own (02h). The registry entry is kept, not
		// replaced.
		entry, ok := r.d.Registry.ByGoal(goal)
		if !ok || entry.GetPane() == nil || paneKey(entry.GetPane()) != paneKey(cmd.GetReusePane()) {
			return nil, failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false, nil,
				"startgoal: re-arm names the goal's own pane")
		}
	}
	st := &state{
		cmd: cmd, spec: spec, goal: goal, harness: harness,
		rearm:        cmd.GetRearmReason() != "",
		reuse:        cmd.GetReusePane() != nil,
		mode:         mode,
		repoShipsOwn: mode == agentv1.WorktreeMode_WORKTREE_MODE_REPO_DRIVER,
		started:      r.d.Now(),
		readyTimeout: timeoutOr(cmd.GetReadyTimeout(), defaultReadyTimeout),
		fileTimeout:  timeoutOr(cmd.GetGoalFileTimeout(), defaultFileTimeout),
	}
	return st, nil
}

// paneKey renders a pane key for comparison; nil renders empty and
// never equals a real pane.
func paneKey(k *typev1.PaneKey) string {
	if k == nil {
		return ""
	}
	return k.GetServer() + "\x00" + k.GetServerStartId() + "\x00" + k.GetPaneId()
}

func timeoutOr(d *durationpb.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	if v := d.AsDuration(); v > 0 {
		return v
	}
	return def
}

// from runs steps from the 1-based index on, in 02h order, skipping the
// steps the path does not take. Every started step reports before it
// runs, so a crash resumes at the step that never proved itself.
func (r *Runner) from(ctx context.Context, st *state, from int, rep Report) *agentv1.CommandResult {
	if st.rearm {
		// The re-arm run keeps the registry entry: the pane and
		// worktree come from it, since steps 2 to 7 never run.
		entry, ok := r.d.Registry.ByGoal(st.goal)
		if !ok {
			return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false, nil,
				"startgoal: re-arm names the goal's own pane")
		}
		st.pane = entry.GetPane()
		st.worktree = entry.GetWorktreePath()
	}
	for i := from; i <= len(steps); i++ {
		name := steps[i-1]
		if !r.wanted(st, name) {
			continue
		}
		report(rep, name, i, false, 0, "")
		if res := r.step(ctx, st, name); res != nil {
			r.reportFail(rep, name, i, res)
			return res
		}
	}
	return r.done(st)
}

// wanted reports whether the step runs on this path (02h: skipped steps
// report nothing).
func (r *Runner) wanted(st *state, name string) bool {
	switch name {
	case "preflight":
		return !st.cmd.GetSkipPreflight()
	case "pick_session", "prepare_branch":
		if st.rearm {
			return false
		}
		if !st.reuse || st.mode == agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH {
			return false
		}
		if name == "prepare_branch" && st.mode == agentv1.WorktreeMode_WORKTREE_MODE_REPO_DRIVER {
			return false
		}
		return true
	case "ensure_worktree":
		if st.rearm || st.reuse {
			return false
		}
		return st.mode == agentv1.WorktreeMode_WORKTREE_MODE_AGENT_ENSURES
	case "ensure_window":
		// New window on the new-window path only: never for a reused
		// pane (its checkout is the workdir), but always for SCRATCH
		// (the scratch directory is the working directory, 02h).
		return !st.rearm && (!st.reuse || st.mode == agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH)
	case "register_goal", "start_log_capture":
		return !st.rearm
	case "set_model":
		return !st.rearm && st.spec.GetModel() != ""
	case "set_effort":
		return !st.rearm && st.spec.GetEffort() != ""
	case "clear_context":
		return !st.rearm && st.spec.GetFreshContext()
	case "write_goal_file":
		return !st.repoShipsOwn
	default:
		return true
	}
}
