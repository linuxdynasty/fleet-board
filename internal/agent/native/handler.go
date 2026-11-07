package native

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Handler is one native command kind's executor handler. All five share one
// Host (one journal, one adapter set); each advertises the host's qualified
// backends, so an unqualified backend stays unadvertised and the executor
// answers its commands UNSUPPORTED before this code runs.
type Handler struct {
	host *Host
	kind string
	wire string
}

// Handlers returns the host as the executor's handlers for the five native
// kinds. Register them and only them: no other handler may run a native
// command, and no native command runs without the journal.
func (h *Host) Handlers() []exec.Handler {
	return []exec.Handler{
		Handler{host: h, kind: string(ring.NativeEffectStart), wire: "StartNativeSession"},
		Handler{host: h, kind: string(ring.NativeEffectSendTurn), wire: "SendNativeTurn"},
		Handler{host: h, kind: string(ring.NativeEffectResume), wire: "ResumeNativeSession"},
		Handler{host: h, kind: string(ring.NativeEffectInterrupt), wire: "InterruptNativeSession"},
		Handler{host: h, kind: string(ring.NativeEffectStop), wire: "StopNativeSession"},
	}
}

// Kind is the Command.kind arm's message name.
func (h Handler) Kind() string { return h.wire }

// Capabilities are the host's qualified backends, or nil while none is
// qualified: unimplemented adapters remain unadvertised.
func (h Handler) Capabilities() []*typev1.Capability { return h.host.Advertised() }

// Class is what the executor does with a native command left running by a
// restart or a failed store write: NotRepeatSafe ends it UNKNOWN_OUTCOME
// without calling Resume, and the journal's recovery pass holds the session
// for explicit reconciliation. A native external call is never repeated
// blindly.
func (h Handler) Class(*agentv1.Command) exec.Class { return exec.NotRepeatSafe }

// Run executes the command through the host: admit, claim, one adapter
// call, resolve. The executor sets the id, times and trace, and scrubs the
// message; result details carry ids, orders and booleans only.
func (h Handler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	if h.host == nil || r.Command == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the handler has no host")
	}
	return h.host.Run(ctx, r.Command)
}

// Resume never runs: Class is NotRepeatSafe, so the executor ends resumed
// running entries UNKNOWN_OUTCOME without calling it. It returns
// UNKNOWN_OUTCOME defensively, never a forged result.
func (h Handler) Resume(context.Context, exec.Request) *agentv1.CommandResult {
	return unknownOutcome("native: native commands never resume; reconcile the held session")
}
