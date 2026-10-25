package worktree

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// Handlers returns the Manager as the executor's handlers for the four
// filesystem kinds: EnsureWorktree and PruneWorktree advertise the
// "worktree" capability, EnsureScratch and CollectScratch the "scratch"
// capability (02e "Command kinds and capabilities"). The run wiring
// registers them; a command whose requires names another capability stays
// unsupported until its own lane lands.
func (m *Manager) Handlers() []exec.Handler {
	return []exec.Handler{
		worktreeHandler{m: m},
		pruneHandler{m: m},
		ensureScratchHandler{m: m},
		collectScratchHandler{m: m},
	}
}

// worktreeHandler is the exec.Handler for EnsureWorktree.
type worktreeHandler struct{ m *Manager }

// Kind is the Command.kind arm's message name.
func (h worktreeHandler) Kind() string { return "EnsureWorktree" }

// Capabilities are advertised in Hello while the handler is registered.
func (h worktreeHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "worktree", Version: 1}}
}

// Class re-runs the command from the start: EnsureWorktree is declarative
// (02e), and a replayed command_id that found its worktree returns it with
// created=false.
func (h worktreeHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h worktreeHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_EnsureWorktree)
	if !ok || cmd.EnsureWorktree == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"worktree: EnsureWorktree handler got another kind")
	}
	return h.m.EnsureWorktree(ctx, cmd.EnsureWorktree)
}

// Resume re-runs from the start: there are no steps to continue from.
func (h worktreeHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// pruneHandler is the exec.Handler for PruneWorktree.
type pruneHandler struct{ m *Manager }

// Kind is the Command.kind arm's message name.
func (h pruneHandler) Kind() string { return "PruneWorktree" }

// Capabilities are advertised in Hello while the handler is registered.
func (h pruneHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "worktree", Version: 1}}
}

// Class re-runs the command from the start: pruning a missing path is a
// no-op OK, so a replayed command_id is safe.
func (h pruneHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h pruneHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_PruneWorktree)
	if !ok || cmd.PruneWorktree == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"worktree: PruneWorktree handler got another kind")
	}
	return h.m.PruneWorktree(ctx, cmd.PruneWorktree)
}

// Resume re-runs from the start: there are no steps to continue from.
func (h pruneHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// ensureScratchHandler is the exec.Handler for EnsureScratch.
type ensureScratchHandler struct{ m *Manager }

// Kind is the Command.kind arm's message name.
func (h ensureScratchHandler) Kind() string { return "EnsureScratch" }

// Capabilities are advertised in Hello while the handler is registered.
func (h ensureScratchHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "scratch", Version: 1}}
}

// Class re-runs the command from the start: EnsureScratch is declarative
// (02e), replacing a file of the same name.
func (h ensureScratchHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h ensureScratchHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_EnsureScratch)
	if !ok || cmd.EnsureScratch == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"worktree: EnsureScratch handler got another kind")
	}
	return h.m.EnsureScratch(ctx, cmd.EnsureScratch)
}

// Resume re-runs from the start: there are no steps to continue from.
func (h ensureScratchHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// collectScratchHandler is the exec.Handler for CollectScratch.
type collectScratchHandler struct{ m *Manager }

// Kind is the Command.kind arm's message name.
func (h collectScratchHandler) Kind() string { return "CollectScratch" }

// Capabilities are advertised in Hello while the handler is registered.
func (h collectScratchHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "scratch", Version: 1}}
}

// Class re-runs the command from the start: CollectScratch only reads, so
// a resumed run changes nothing.
func (h collectScratchHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h collectScratchHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_CollectScratch)
	if !ok || cmd.CollectScratch == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"worktree: CollectScratch handler got another kind")
	}
	return h.m.CollectScratch(ctx, cmd.CollectScratch)
}

// Resume re-runs from the start: there are no steps to continue from.
func (h collectScratchHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
