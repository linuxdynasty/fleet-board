package startgoal

import (
	"context"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
)

// step runs one wanted step. A nil return is OK; anything else ends the
// composite and types nothing further.
func (r *Runner) step(ctx context.Context, st *state, name string) *agentv1.CommandResult {
	switch name {
	case "preflight":
		return r.preflight(ctx, st)
	case "pick_session":
		return r.pickSession(ctx, st)
	case "prepare_branch":
		return r.prepareBranch(ctx, st)
	case "ensure_worktree":
		return r.ensureWorktree(ctx, st)
	case "ensure_window":
		return r.ensureWindow(ctx, st)
	case "register_goal":
		return r.registerGoal(ctx, st)
	case "start_log_capture":
		return r.startCapture(ctx, st)
	case "wait_ready":
		return r.waitReady(ctx, st)
	case "set_model":
		return r.setModel(ctx, st)
	case "set_effort":
		return r.setEffort(ctx, st)
	case "clear_context":
		return r.clearContext(ctx, st)
	case "write_goal_file":
		return r.writeGoalFile(ctx, st)
	case "type_goal":
		return r.typeGoal(ctx, st)
	case "confirm_goal_file":
		return r.confirmGoalFile(ctx, st)
	default:
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil, "startgoal: unknown step "+name)
	}
}

// preflight runs the checks the agent can answer locally; the hub's own
// preflight (sign-in, push access freshness, slots, pacing) runs through
// the standalone Preflight command or the skip_preflight fast path.
// Every failure types nothing.
func (r *Runner) preflight(ctx context.Context, st *state) *agentv1.CommandResult {
	n := len(r.d.Registry.Entries())
	if st.rearm {
		// The re-armed goal keeps its entry, pane and worktree: it
		// consumes no new slot, so its own binding does not count
		// toward the cap (02h re-arm).
		if _, ok := r.d.Registry.ByGoal(st.goal); ok {
			n--
		}
	}
	if n >= r.d.MaxGoals() {
		return failed(typev1.ReasonCode_REASON_CODE_AGENT_GOAL_CAP_REACHED, true, nil,
			"startgoal: the machine already runs its goal cap")
	}
	if free, ok := r.d.DiskFree(); !ok || free < lowDiskBytes {
		return failed(typev1.ReasonCode_REASON_CODE_DISK_LOW, true, nil,
			"startgoal: free disk is under the 5 GiB low point")
	}
	top, err := r.d.Panes.LookupRepo(st.spec.GetRepo())
	if err != nil || top == "" {
		if st.mode == agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH {
			return nil
		}
		return failed(typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING, false, nil,
			"startgoal: no local checkout of that repo")
	}
	if st.mode == agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH {
		return nil
	}
	if push, known := r.d.Panes.PushAccess(ctx, st.spec.GetRepo()); known && !push {
		return failed(typev1.ReasonCode_REASON_CODE_PUSH_ACCESS_MISSING, false, nil,
			"startgoal: the forge login cannot push to that repo")
	}
	if st.reuse {
		ps, err := r.d.Panes.Examine(ctx, st.cmd.GetReusePane())
		if err != nil {
			return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false, nil,
				"startgoal: the reuse pane is gone")
		}
		if ps.TrustBox {
			return failed(typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED, false, nil,
				"startgoal: the reuse pane waits on folder trust")
		}
	}
	return nil
}

// pickSession re-checks the hub's reuse pane just before typing (02h):
// the agent never falls back to a new window silently.
func (r *Runner) pickSession(ctx context.Context, st *state) *agentv1.CommandResult {
	want := st.cmd.GetReusePane()
	ps, err := r.d.Panes.Examine(ctx, want)
	if err != nil || !ps.Exists {
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false, nil,
			"startgoal: the reuse pane is gone")
	}
	if ps.BoundGoal != "" && ps.BoundGoal != st.goal {
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false,
			map[string]string{"why": "pane_bound"},
			"startgoal: the reuse pane runs another live goal")
	}
	if ps.Harness != "" && ps.Harness != st.harness {
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false, nil,
			"startgoal: the reuse pane runs another harness")
	}
	if ps.CheckoutHeld {
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false,
			map[string]string{"why": "checkout_in_use"},
			"startgoal: another goal already works in that checkout")
	}
	top, err := r.d.Panes.LookupRepo(st.spec.GetRepo())
	if err != nil || top == "" {
		return failed(typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING, false, nil,
			"startgoal: no local checkout of that repo")
	}
	if ps.CheckoutTop == "" || !sameDir(ps.PaneDir, top) {
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_WORKDIR_MISMATCH, false, nil,
			"startgoal: the reuse pane works in another repo or a subfolder")
	}
	if ps.Dirty {
		return failed(typev1.ReasonCode_REASON_CODE_CHECKOUT_DIRTY, true, nil,
			"startgoal: the reuse checkout has uncommitted changes")
	}
	if ps.TrustBox {
		return failed(typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED, false, nil,
			"startgoal: the reuse pane waits on folder trust")
	}
	if ps.InputDraft || !ps.Ready || !ps.InputEmpty {
		return failed(typev1.ReasonCode_REASON_CODE_PANE_BUSY, true, nil,
			"startgoal: the reuse pane is not at an empty idle prompt")
	}
	st.pane = want
	st.session = ps.Session
	st.reused = true
	st.worktree = top
	return nil
}

// prepareBranch starts the goal branch in the reuse checkout (02h,
// AGENT_ENSURES only; the driver makes its own). Declarative: already
// on the branch is OK.
func (r *Runner) prepareBranch(ctx context.Context, st *state) *agentv1.CommandResult {
	branch := st.spec.GetBranch()
	if branch == "" {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil,
			"startgoal: a reused pane needs the spec branch")
	}
	head, err := r.d.Git.SwitchBranch(ctx, st.worktree, branch, "")
	if err != nil {
		if errorsIsDirty(err) {
			return failed(typev1.ReasonCode_REASON_CODE_CHECKOUT_DIRTY, true, nil,
				"startgoal: the reuse checkout turned dirty")
		}
		return failed(typev1.ReasonCode_REASON_CODE_GIT_FAILED, false, nil,
			"startgoal: the branch switch failed")
	}
	st.branch = branch
	st.head = head
	return nil
}

// ensureWorktree creates the goal's worktree through the row-20 H2
// manager (AGENT_ENSURES only). The manager's own result maps the
// failure; an existing worktree on the same branch reuses it.
func (r *Runner) ensureWorktree(ctx context.Context, st *state) *agentv1.CommandResult {
	res := r.d.Trees.EnsureWorktree(ctx, &agentv1.EnsureWorktree{
		Repo: st.spec.GetRepo(), GoalId: st.spec.GetGoalId(),
		Branch: st.spec.GetBranch(), BaseRef: "",
	})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		return res
	}
	got := res.GetEnsureWorktree()
	st.worktree = got.GetPath()
	st.branch = got.GetBranch().GetBranch()
	st.head = got.GetBranch().GetHeadCommit()
	return nil
}

// ensureWindow opens the goal window running the harness (a new session
// when the target is empty; the scratch directory is the cwd for
// SCRATCH goals), tagged with the goal id for idempotent resume.
func (r *Runner) ensureWindow(ctx context.Context, st *state) *agentv1.CommandResult {
	dir := st.worktree
	if st.mode == agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH {
		scratch, res := r.d.Trees.EnsureScratchDir(ctx, st.goal)
		if res != nil {
			return res
		}
		dir = scratch
		st.worktree = scratch
	}
	if dir == "" {
		if st.mode == agentv1.WorktreeMode_WORKTREE_MODE_AGENT_ENSURES && !st.reuse {
			// Resume gap: step 4 made the worktree but step 6 never
			// recorded it, so there is nothing to recover by name.
			// Rebuild it by branch (idempotent: the manager reuses an
			// existing worktree on the branch) instead of running the
			// goal in the main checkout.
			if res := r.ensureWorktree(ctx, st); res != nil {
				return res
			}
			dir = st.worktree
		} else {
			top, err := r.d.Panes.LookupRepo(st.spec.GetRepo())
			if err != nil || top == "" {
				return failed(typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING, false, nil,
					"startgoal: no local checkout of that repo")
			}
			dir = top
			st.worktree = top
		}
	}
	name := st.cmd.GetWindowName()
	if name == "" {
		// Template "{harness} {work}" (03c), where work is the
		// WorkItemRef display ("eshu#7398"): untrusted hub text, so
		// control characters come off and the name stays short.
		work := goalfile.Clean(st.spec.GetWorkItem().GetDisplay(), 32)
		work = strings.Join(strings.Fields(work), " ")
		name = st.harness + " " + strings.TrimSpace(work)
	}
	w, res := r.d.Windows.Start(ctx, st.cmd.GetInSession(), st.spec.GetSessionId().GetValue(),
		name, dir, st.harness, st.goal, st.spec.GetProfile())
	if res != nil {
		return res
	}
	st.pane = w.Pane
	st.session = w.Session
	st.reused = w.Reused
	return nil
}

// registerGoal writes the registry entry every later fact keys on
// (02h): after the pane exists, before the goal is typed.
func (r *Runner) registerGoal(_ context.Context, st *state) *agentv1.CommandResult {
	if _, err := r.d.Registry.Register(st.pane, st.goal, st.harness, st.worktree,
		st.mode == agentv1.WorktreeMode_WORKTREE_MODE_REPO_DRIVER); err != nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
			"startgoal: the goal registry refused the entry")
	}
	return nil
}

// startCapture starts pipe-pane capture of the goal pane (K1).
func (r *Runner) startCapture(ctx context.Context, st *state) *agentv1.CommandResult {
	if err := r.d.Capture.Start(ctx, st.pane, st.goal); err != nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
			"startgoal: log capture did not start")
	}
	return nil
}

// waitReady polls the pane screen until the harness shows its proven
// idle prompt with an empty input line. A trust box or a startup
// screen fails fast with its own reason; a screen that never settles
// reads READY_TIMEOUT.
func (r *Runner) waitReady(ctx context.Context, st *state) *agentv1.CommandResult {
	deadline := time.Now().Add(st.readyTimeout)
	for {
		screen, err := r.d.Screens.Capture(ctx, st.pane.GetServer(), st.pane.GetPaneId())
		if err == nil {
			switch c := probe.Classify(st.harness, screen); {
			case c.Outcome == probe.OutcomeTrust:
				return failed(typev1.ReasonCode_REASON_CODE_TRUST_PROMPT_SEEN, false, nil,
					"startgoal: the harness waits on folder trust")
			case c.Outcome == probe.OutcomeStartup:
				meta := map[string]string{}
				if c.Kind != "" {
					meta["kind"] = c.Kind
				}
				return failed(typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN, false, meta,
					"startgoal: a startup screen shows; nothing typed")
			case c.Outcome == probe.OutcomeReady && c.Input == probe.InputEmpty:
				return nil
			}
		}
		if time.Now().After(deadline) {
			return failed(typev1.ReasonCode_REASON_CODE_READY_TIMEOUT, true, nil,
				"startgoal: the harness never reached its idle prompt")
		}
		if err := sleepCtx(ctx, r.d.PollReady); err != nil {
			return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
				"startgoal: the wait ended with the command context")
		}
	}
}
