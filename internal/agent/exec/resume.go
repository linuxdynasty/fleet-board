package exec

import (
	"context"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// resumeOpen is the restart pass (ruling fork 4 iii). It runs once per run,
// on the envelope goroutine, after the first stream's snapshot and unacked
// results are out, because deadlines need hub time. Each entry with no
// result that no goroutine holds is deadline-checked on the strict clock:
// past, it ends EXPIRED (received or running); a received entry then goes
// through the checks after the log and runs; a running one is run again,
// resumed per step or ended UNKNOWN_OUTCOME as its handler's class says.
func (e *Executor) resumeOpen(ctx context.Context) {
	open, err := e.opts.DB.Open(ctx)
	if err != nil {
		e.storeFailed(ctx, "", "open", err)
		return
	}
	for _, ent := range open {
		if e.isRunning(ent.ID) {
			continue
		}
		c := &agentv1.Command{}
		if len(ent.Body) == 0 || proto.Unmarshal(ent.Body, c) != nil {
			c = &agentv1.Command{CommandId: &typev1.CommandId{Value: ent.ID}}
			e.start(ctx, c, ent, plan{refusal: &agentv1.CommandResult{
				Status: typev1.Status_STATUS_UNKNOWN_OUTCOME, Message: "the logged command cannot be read after a restart",
			}})
			continue
		}
		if e.past(c) {
			e.start(ctx, c, ent, plan{refusal: refusal(typev1.Status_STATUS_EXPIRED, typev1.ReasonCode_REASON_CODE_EXPIRED,
				"the command's deadline passed while the agent was stopped")})
			continue
		}
		e.start(ctx, c, ent, e.decide(c, ent.Kind, ent, true))
	}
}
