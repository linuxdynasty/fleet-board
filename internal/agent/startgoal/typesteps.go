package startgoal

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// setModel types the harness's /model (SESSION class) when the spec
// names one. The type is the verification today's parsers allow: no
// model-line parser exists yet, so the wanted model is typed as asked
// and reported back as effective; a model-line reader refines this step
// when it lands.
func (r *Runner) setModel(ctx context.Context, st *state) *agentv1.CommandResult {
	if commandClass(st.harness, "/model") != typev1.CommandClass_COMMAND_CLASS_SESSION {
		return failed(typev1.ReasonCode_REASON_CODE_MODEL_NOT_AVAILABLE, false, nil,
			"startgoal: the harness has no /model command")
	}
	if res := r.d.Typer.Type(ctx, st.pane, "/model",
		typev1.CommandClass_COMMAND_CLASS_SESSION, st.spec.GetModel()); res != nil {
		return res
	}
	return nil
}

// setEffort types the harness's /effort the same way.
func (r *Runner) setEffort(ctx context.Context, st *state) *agentv1.CommandResult {
	if commandClass(st.harness, "/effort") != typev1.CommandClass_COMMAND_CLASS_SESSION {
		return failed(typev1.ReasonCode_REASON_CODE_EFFORT_NOT_SUPPORTED, false, nil,
			"startgoal: the harness has no /effort command")
	}
	if res := r.d.Typer.Type(ctx, st.pane, "/effort",
		typev1.CommandClass_COMMAND_CLASS_SESSION, st.spec.GetEffort()); res != nil {
		return res
	}
	return nil
}

// clearContext types /clear (DESTRUCTIVE) in a reused pane, only in a
// session the fleet started or a pane the owner marked with
// fresh_context_on_reuse. Anywhere else is FRESH_CONTEXT_NOT_ALLOWED:
// /clear is never typed into the developer's own context unless they
// chose it (02g).
func (r *Runner) clearContext(ctx context.Context, st *state) *agentv1.CommandResult {
	if !st.reused {
		return failed(typev1.ReasonCode_REASON_CODE_FRESH_CONTEXT_NOT_ALLOWED, false, nil,
			"startgoal: fresh context is a reuse-pane option")
	}
	ps, err := r.d.Panes.Examine(ctx, st.pane)
	if err != nil || !ps.Exists {
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE, false, nil,
			"startgoal: the reuse pane is gone")
	}
	if !ps.FleetSession && !ps.FreshMark {
		return failed(typev1.ReasonCode_REASON_CODE_FRESH_CONTEXT_NOT_ALLOWED, false, nil,
			"startgoal: that pane keeps its context without the owner's mark")
	}
	if res := r.d.Typer.Type(ctx, st.pane, "/clear",
		typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE, ""); res != nil {
		return res
	}
	return nil
}

// goalContent renders the initial goal file: the objective as the
// first line (a WORKING state, 01d2) plus the CONSENT: line (03c). A
// re-arm rewrites the first line as the objective plus the reason.
func goalContent(st *state) []byte {
	words := make([]string, 0, len(st.spec.GetConsent()))
	for _, act := range st.spec.GetConsent() {
		w, err := ConsentWord(act)
		if err != nil {
			continue
		}
		words = append(words, w)
	}
	first := st.spec.GetGoalText()
	if st.rearm {
		first += " reopened: " + st.cmd.GetRearmReason()
	}
	var b strings.Builder
	b.WriteString(first + "\n")
	b.WriteString("CONSENT: " + strings.Join(words, ",") + "\n")
	raw := b.String()
	if len(raw) > 4096 {
		raw = raw[:4096]
	}
	return []byte(raw)
}

// writeGoalFile creates .fleet-board/ and goal-<id> through no-follow
// opens (01d2 step 12). Declarative: the same content again is OK, so a
// resumed run replays it safely. An empty worktree refuses: the writer
// would resolve it against the process directory, never a goal folder.
func (r *Runner) writeGoalFile(_ context.Context, st *state) *agentv1.CommandResult {
	if st.worktree == "" {
		return failed(typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED, false, nil,
			"startgoal: no worktree holds the goal file")
	}
	if _, err := r.d.Files.EnsureGoalFile(st.worktree, st.goal, goalContent(st), false); err != nil {
		return failed(typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED, false,
			map[string]string{"why": goalfile.Why(err)},
			"startgoal: the goal file write failed")
	}
	return nil
}

// typeGoal types the goal line through RunHarnessCommand at a proven
// idle prompt with an empty input line, then Enter. Never retyped after
// a restart: Resume proves it through the goal file instead.
func (r *Runner) typeGoal(ctx context.Context, st *state) *agentv1.CommandResult {
	line, err := RenderGoalLine(st.harness, st.spec.GetConsent(), st.spec.GetGoalText(),
		st.spec.GetDispatchReason(), st.repoShipsOwn)
	if err != nil {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, nil,
			"startgoal: the goal line cannot be built")
	}
	if commandClass(st.harness, "/goal") != typev1.CommandClass_COMMAND_CLASS_GOAL {
		return failed(typev1.ReasonCode_REASON_CODE_GOAL_SUBCOMMAND_UNSUPPORTED, false, nil,
			"startgoal: the harness has no goal subcommand")
	}
	if res := r.d.Typer.Type(ctx, st.pane, line,
		typev1.CommandClass_COMMAND_CLASS_GOAL, ""); res != nil {
		return res
	}
	st.typedLine = line
	return nil
}

// confirmGoalFile watches for the goal file within goal_file_timeout
// (read-only, 01d2 step 14). For a repo-owned checkout it watches the
// repo's own file (newer than the start); anything else re-reads the
// file the agent wrote. A file not seen in time fails the start.
func (r *Runner) confirmGoalFile(ctx context.Context, st *state) *agentv1.CommandResult {
	deadline := time.Now().Add(st.fileTimeout)
	for {
		if r.proven(ctx, st) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
				"startgoal: the wait ended with the command context")
		}
		if time.Now().After(deadline) {
			return failed(typev1.ReasonCode_REASON_CODE_GOAL_FILE_NOT_SEEN, false, nil,
				"startgoal: the goal file never appeared")
		}
		if err := sleepCtx(ctx, r.d.PollFile); err != nil {
			return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
				"startgoal: the wait ended with the command context")
		}
	}
}

// locate fills the pane and worktree from the registry entry or the
// command, without side effects. Missing facts stay missing: callers
// fail closed on them.
func (r *Runner) locate(ctx context.Context, st *state) {
	if st.reuse {
		st.pane = st.cmd.GetReusePane()
		if ps, err := r.d.Panes.Examine(ctx, st.pane); err == nil && ps.Exists {
			st.session = ps.Session
			st.reused = true
			st.worktree = ps.CheckoutTop
		}
		return
	}
	if entry, ok := r.d.Registry.ByGoal(st.goal); ok {
		st.pane = entry.GetPane()
		st.worktree = entry.GetWorktreePath()
	}
}

// proven reports whether the goal file shows this goal's id: the file
// the agent wrote reads back, or (repo-owned) the repo's own eshu file
// is newer than the run start. An empty worktree proves nothing.
func (r *Runner) proven(_ context.Context, st *state) bool {
	if st.worktree == "" {
		return false
	}
	if st.repoShipsOwn {
		_, mod, ok, err := goalfile.Select(st.worktree, st.started)
		return err == nil && ok && !mod.Before(st.started)
	}
	_, err := r.d.Files.ReadGoalFile(st.worktree, st.goal)
	return err == nil
}

// rederive rebuilds the run state for a resume at step from without
// side effects: the pane and worktree come from the registry entry the
// earlier attempt wrote (or the command for early steps), the branch
// from the spec, HEAD from one read-only git call.
func (r *Runner) rederive(ctx context.Context, st *state, from int) error {
	r.locate(ctx, st)
	if st.reuse {
		if st.worktree == "" {
			return fmt.Errorf("startgoal: the reuse pane is gone")
		}
	} else if from > stepIndex("register_goal") && st.worktree == "" {
		return fmt.Errorf("startgoal: the goal registry lost the entry")
	}
	st.branch = st.spec.GetBranch()
	if line, err := RenderGoalLine(st.harness, st.spec.GetConsent(), st.spec.GetGoalText(),
		st.spec.GetDispatchReason(), st.repoShipsOwn); err == nil {
		st.typedLine = line
	}
	return nil
}

// done builds the StartGoalResult: the pane, session, worktree, branch,
// the typed line, the effective model and effort, the registration,
// and the first GoalState the confirm step read back.
func (r *Runner) done(st *state) *agentv1.CommandResult {
	entry, _ := r.d.Registry.ByPane(st.pane)
	file, _ := r.d.Files.ReadGoalFile(st.worktree, st.goal)
	goal := &agentv1.GoalState{
		GoalId: st.spec.GetGoalId(), Pane: st.pane,
		FirstLine: file.FirstLine, ConsentLine: file.ConsentLine,
		State: file.State, WatchPid: file.WatchPID, Owner: file.Owner, Action: file.Action,
		ObservedTime: timestamppb.New(r.d.Now()),
	}
	if st.session != nil {
		goal.SessionId = st.session.GetSessionId()
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("startgoal: goal %s started", st.goal),
		Detail: &agentv1.CommandResult_StartGoal{
			StartGoal: &agentv1.StartGoalResult{
				Pane: st.pane, Session: st.session, ReusedSession: st.reused,
				WorktreePath: st.worktree, Goal: goal, TypedGoalLine: st.typedLine,
				Branch: &agentv1.BranchState{
					Repo: st.spec.GetRepo(), Path: st.worktree, GoalId: st.spec.GetGoalId(),
					Branch: st.branch, HeadCommit: st.head, ObservedTime: timestamppb.New(r.d.Now()),
				},
				Model: st.spec.GetModel(), Effort: st.spec.GetEffort(),
				Registration: entry,
			},
		},
	}
}

// fail reports one FAILED step and ends the composite with its reason.
// Nothing after the failure runs and nothing further is typed.
func (r *Runner) fail(step string, st typev1.Status, reason typev1.ReasonCode,
	retryable bool, meta map[string]string, msg string, rep Report,
) *agentv1.CommandResult {
	res := failed(reason, retryable, meta, msg)
	res.Status = st
	r.reportFail(rep, step, stepIndex(step), res)
	return res
}

// failed builds a FAILED result. retryable follows the contract: true
// only when the hub may send the kind again and expect it to work.
func failed(reason typev1.ReasonCode, retryable bool, meta map[string]string, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_FAILED, Reason: reason,
		Message: msg, Retryable: retryable, Metadata: meta,
	}
}

func report(rep Report, step string, index int, failed bool, reason typev1.ReasonCode, msg string) {
	if rep == nil {
		return
	}
	rep(step, index, failed, reason, msg)
}

func (r *Runner) reportFail(rep Report, step string, index int, res *agentv1.CommandResult) {
	report(rep, step, index, true, res.GetReason(), res.GetMessage())
}

// sameDir compares two checkout paths after best-effort symlink
// resolution; unresolvable sides compare lexically.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

// errorsIsDirty reports whether a branch switch failed on dirt. The Git
// seam returns a fail-closed error; production wraps ErrDirty.
func errorsIsDirty(err error) bool {
	return err != nil && strings.Contains(err.Error(), "dirty")
}

// sleepCtx waits out the poll gap, cut short by the command context.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
