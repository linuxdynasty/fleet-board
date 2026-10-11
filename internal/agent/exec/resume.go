package exec

import (
	"context"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// resumeOpen is the restart pass (ruling fork 4 iii). It runs once per run,
// on the envelope goroutine, after the first stream's snapshot and unacked
// results are out, because deadlines need hub time. The list it reads can
// be stale by the time it reaches an entry: a command this run took may
// have finished since. So each entry is claimed first and read again; one
// another goroutine holds, or that has its result now, is skipped. The
// rest are deadline-checked on the strict clock: past, it ends EXPIRED
// (received or running); a received entry then goes through the checks
// after the log and runs; a running one is run again, resumed per step or
// ended UNKNOWN_OUTCOME as its handler's class says.
func (e *Executor) resumeOpen(ctx context.Context) {
	open, err := e.opts.DB.Open(ctx)
	if err != nil {
		e.storeFailed(ctx, "", "open", err)
		return
	}
	for _, listed := range open {
		if !e.claim(listed.ID, listed.Kind) {
			continue
		}
		ent, err := e.opts.DB.Command(ctx, listed.ID)
		if err != nil {
			e.release(listed.ID)
			e.storeFailed(ctx, listed.ID, "read", err)
			continue
		}
		if ent.Status == ring.StatusFinished {
			e.release(ent.ID)
			continue
		}
		c, p := e.resumeCommand(ent)
		e.launch(ctx, c, ent, p)
	}
}

// resumeCommand decodes a claimed open entry and decides what to do with it.
func (e *Executor) resumeCommand(ent ring.Entry) (*agentv1.Command, plan) {
	c := &agentv1.Command{}
	if len(ent.Body) == 0 || proto.Unmarshal(ent.Body, c) != nil {
		c = &agentv1.Command{CommandId: &typev1.CommandId{Value: ent.ID}}
		return c, plan{refusal: &agentv1.CommandResult{
			Status: typev1.Status_STATUS_UNKNOWN_OUTCOME, Message: "the logged command cannot be read after a restart",
		}}
	}
	if e.past(c) {
		return c, plan{refusal: refusal(typev1.Status_STATUS_EXPIRED, typev1.ReasonCode_REASON_CODE_EXPIRED,
			"the command's deadline passed while the agent was stopped")}
	}
	return c, e.decide(c, ent.Kind, ent, true)
}
