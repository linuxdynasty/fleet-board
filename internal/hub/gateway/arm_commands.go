package gateway

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// The command arms (plan row 5, PR 5f): what the agent answers to a
// Command. Each runs on the stream's ingest goroutine, in arrival order;
// the outbox bounds and scrubs the free text there, then applies the
// message in one writer job.
func init() {
	arms.add("command_ack", func(ctx context.Context, a *agentService, s *stream, m *agentv1.ConnectRequest) {
		wctx, cancel := commandCtx(ctx)
		defer cancel()
		_, _ = a.outbox.Ack(wctx, a.from(s), m.GetCommandAck()) // the outbox logs and counts every outcome
	})
	arms.add("command_progress", func(ctx context.Context, a *agentService, s *stream, m *agentv1.ConnectRequest) {
		wctx, cancel := commandCtx(ctx)
		defer cancel()
		_, _ = a.outbox.Progress(wctx, a.from(s), m.GetCommandProgress())
	})
	arms.add("command_result", commandResult)
}

// commandResult stores the result and, once that committed (or the result
// was already stored), answers ResultAck so the agent may forget it. A
// refused result is never answered. A ResultAck the stream cannot take
// (it ended) is not retried: the agent re-sends the result after its next
// Snapshot and the hub answers that one.
func commandResult(ctx context.Context, a *agentService, s *stream, m *agentv1.ConnectRequest) {
	r := m.GetCommandResult()
	wctx, cancel := commandCtx(ctx)
	defer cancel()
	out, err := a.outbox.Result(wctx, a.from(s), r)
	if err != nil || (out != outbox.Applied && out != outbox.Known) {
		return
	}
	ack := &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_ResultAck{ResultAck: &agentv1.ResultAck{
		CommandIds: []*typev1.CommandId{{Value: r.GetCommandId().GetValue()}},
	}}}
	if !s.queue(ctx, ack) {
		a.log.InfoContext(ctx, "outbox.result_ack_unsent", "command_id", r.GetCommandId().GetValue(),
			"machine_id", s.machineID, "epoch", s.epoch)
	}
}
