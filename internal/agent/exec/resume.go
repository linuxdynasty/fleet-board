package exec

import (
	"context"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// resumeOpen is the resume pass (6e round-2 ruling D; ruling fork 4 iii).
// It runs at every connect, on the envelope goroutine, once the stream's
// snapshot and unacked results are out (deadlines need hub time), and takes
// up every unfinished entry no goroutine holds: one left by a restart, or
// by a store write that failed (RecordResult, SetRunning) in this run. It
// reads no timer and retries nothing on its own; a connect is its retry.
// The list it reads can be stale by the time it reaches an entry, so each
// one goes through startResumed.
func (e *Executor) resumeOpen(ctx context.Context) {
	open, err := e.opts.DB.Open(ctx)
	if err != nil {
		e.storeFailed(ctx, "", "open", err)
		return
	}
	for _, listed := range open {
		e.startResumed(ctx, listed)
	}
}

// startResumed claims a listed entry, reads it again, and skips it when a
// goroutine holds it or it has its result now; otherwise the fresh entry
// (received or running may have changed) decides what runs. Past its
// deadline it ends EXPIRED; a received entry goes through the checks after
// the log, its local approval included even if one was given before a
// failed write (asked again: conservative); a running one is run again,
// resumed per step or ended UNKNOWN_OUTCOME as its handler's class says.
func (e *Executor) startResumed(ctx context.Context, listed ring.Entry) {
	if !e.claim(listed.ID, listed.Kind) {
		return
	}
	fresh, err := e.opts.DB.Command(ctx, listed.ID)
	if err != nil {
		e.release(listed.ID)
		e.storeFailed(ctx, listed.ID, "read", err)
		return
	}
	if fresh.Status == ring.StatusFinished {
		e.release(fresh.ID)
		return
	}
	c, p := e.resumeCommand(ctx, fresh)
	e.launch(ctx, c, fresh, p)
}

// resumeCommand decodes a claimed open entry and decides what to do with it.
func (e *Executor) resumeCommand(ctx context.Context, ent ring.Entry) (*agentv1.Command, plan) {
	c := &agentv1.Command{}
	if len(ent.Body) == 0 || proto.Unmarshal(ent.Body, c) != nil {
		c = &agentv1.Command{CommandId: &typev1.CommandId{Value: ent.ID}}
		return c, plan{refusal: &agentv1.CommandResult{
			Status: typev1.Status_STATUS_UNKNOWN_OUTCOME, Message: "the logged command cannot be read back",
		}}
	}
	if e.past(c) {
		return c, plan{refusal: refusal(typev1.Status_STATUS_EXPIRED, typev1.ReasonCode_REASON_CODE_EXPIRED,
			"the command's deadline passed before the agent could take it up again")}
	}
	return c, e.decide(ctx, c, ent.Kind, ent, true)
}
