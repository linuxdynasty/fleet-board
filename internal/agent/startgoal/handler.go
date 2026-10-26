package startgoal

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// Handler is the exec.Handler for StartGoal (capability goal.start,
// 02e). One command_id runs once: the executor never replays a logged
// id, so a replayed StartGoal returns its recorded result without
// typing again.
type Handler struct {
	run *Runner
}

// NewHandler builds the StartGoal handler on wired seams.
func NewHandler(d Deps) (*Handler, error) {
	r, err := New(d)
	if err != nil {
		return nil, err
	}
	return &Handler{run: r}, nil
}

// Kind is the Command.kind arm's message name.
func (h *Handler) Kind() string { return "StartGoal" }

// Capabilities advertises goal.start version 1 while registered.
func (h *Handler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "goal.start", Version: 1}}
}

// Class resumes per step: only the handler knows which steps are safe
// to continue (02h restart classes).
func (h *Handler) Class(*agentv1.Command) exec.Class { return exec.PerStep }

// Run executes the 14 steps, reporting each start and the one failure.
func (h *Handler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_StartGoal)
	if !ok || cmd.StartGoal == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
			"startgoal: StartGoal handler got another kind")
	}
	return h.run.Run(ctx, cmd.StartGoal, progressOf(r))
}

// Resume continues at the persisted step: declarative steps re-run
// safely, while clear_context and type_goal without proof end
// UNKNOWN_OUTCOME.
func (h *Handler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_StartGoal)
	if !ok || cmd.StartGoal == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, false, nil,
			"startgoal: StartGoal handler got another kind")
	}
	return h.run.Resume(ctx, cmd.StartGoal, r.Step, progressOf(r))
}

// progressOf adapts an exec progress report to a step Report: each
// start reports, and the one failure reports as FAILED (which ends the
// composite, command.proto).
func progressOf(r exec.Request) Report {
	if r.Progress == nil {
		return nil
	}
	return func(step string, index int, failed bool, reason typev1.ReasonCode, msg string) {
		st := typev1.Status_STATUS_UNSPECIFIED
		if failed {
			st = typev1.Status_STATUS_FAILED
		}
		p := &agentv1.CommandProgress{
			//nolint:gosec // G115: a step index is 1 to 14
			Step: step, StepIndex: uint32(index), StepCount: uint32(len(steps)),
			Status: st, Reason: reason, Message: msg,
		}
		_ = r.Progress(context.Background(), p)
	}
}
