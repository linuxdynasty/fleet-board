package settings

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// maxEpoch caps a settings_epoch the agent takes.
const maxEpoch = 128

// handle is one Settings on Run's goroutine: the order check (02-contracts
// "Settings order"), the merge, the save, then the ack.
func (m *Manager) handle(ctx context.Context, it item) {
	m.opts.Local.Recheck(ctx)
	s, cur := it.s, m.cur.Load()
	epoch, gen := s.GetSettingsEpoch(), s.GetGeneration()
	switch {
	case epoch == "" || !printable(epoch, maxEpoch):
		m.ignored(ctx, "no_epoch", gen, cur)
		return
	case m.opts.Local.SafeMode():
		// Nothing is kept and the view stays the defaults; Hello goes on
		// reporting the held generation, so the hub sends its Settings again
		// once the file is fixed (03e2).
		out := Merge(Input{Hub: s, Local: m.opts.Local.Policy(), SafeMode: true})
		m.report(ctx, out, true)
		m.send(ctx, it.ctx, out.Ack)
		return
	case cur.held && epoch == cur.epoch && gen == cur.gen:
		m.ignored(ctx, "repeat", gen, cur)
		m.send(ctx, it.ctx, cur.ack)
		return
	case cur.held && epoch == cur.epoch && gen < cur.gen:
		m.ignored(ctx, "older", gen, cur)
		m.send(ctx, it.ctx, cur.ack)
		return
	}
	mctx, span := m.m.tracer.Start(parent(ctx, s), "agent.settings.merge",
		trace.WithAttributes(attribute.Int64("generation", int64(min(gen, 1<<62)))))
	defer span.End()
	out := Merge(Input{Hub: s, Prev: cur.kept, Local: m.opts.Local.Policy()})
	if err := m.save(mctx, out); err != nil {
		span.RecordError(err)
		m.m.storeFailed(mctx)
		m.log.WarnContext(mctx, "settings.store_failed", "err_kind", errKind(err))
		return // no ack: the hub sends Settings again (03e state machine)
	}
	m.cur.Store(newView(true, out.Kept, out.Ack))
	m.report(mctx, out, false)
	m.send(mctx, it.ctx, out.Ack)
}

// save writes the kept settings and the ack in one store job, before the
// ack is queued (03e "Persistence").
func (m *Manager) save(ctx context.Context, out Outcome) error {
	kept, err := marshal(out.Kept)
	if err != nil {
		return err
	}
	ack, err := marshal(out.Ack)
	if err != nil {
		return err
	}
	return m.opts.DB.SaveSettings(ctx, ring.Held{
		Epoch: out.Kept.GetSettingsEpoch(), Generation: out.Kept.GetGeneration(), Settings: kept, Ack: ack,
		PolicyHash: m.opts.Local.Info().GetHash(), Applied: m.now(),
	})
}

// report logs and counts what one merge did. Values are logged only for a
// number, a duration or a mode; never a list (it may hold paths).
func (m *Manager) report(ctx context.Context, out Outcome, safe bool) {
	a := out.Ack
	m.log.InfoContext(ctx, "settings.applied", "epoch", a.GetSettingsEpoch(), "generation", a.GetGeneration(),
		"capped", len(a.GetCappedFields()), "rejected", len(a.GetRejected()),
		"unrecognized", len(a.GetUnrecognizedValueFields())+len(a.GetUnrecognizedSettingsFields()), "safe_mode", safe)
	for _, c := range out.Capped {
		args := []any{"field", c.Field}
		if c.Hub != "" {
			args = append(args, "hub_value", c.Hub, "local_ceiling", c.Local)
		}
		m.log.InfoContext(ctx, "settings.capped", args...)
		m.m.capped(ctx, c.Field)
	}
	for _, r := range a.GetRejected() {
		field := r.GetField()
		if len(field) > len(fProbeByAccount) && field[:len(fProbeByAccount)] == fProbeByAccount {
			field = fProbeByAccount // the account id is not a label value
		}
		m.log.WarnContext(ctx, "settings.rejected", "field", field, "reason", r.GetReason().String())
		m.m.rejected(ctx, field, r.GetReason().String())
	}
}

func (m *Manager) ignored(ctx context.Context, reason string, gen uint64, cur *view) {
	m.m.ignored(ctx, reason)
	m.log.InfoContext(ctx, "settings.ignored", "reason", reason, "generation", gen, "held_generation", cur.gen)
}

// send queues ack for the stream the Settings came on. It may wait for room
// (this is not the sending goroutine), but not past that stream's end: the
// context keeps the stream's cancel but not the hook's mark, which would
// make Send refuse at once.
func (m *Manager) send(ctx, stream context.Context, ack *agentv1.SettingsAck) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(stream, cancel)
	defer stop()
	err := m.opts.Sender.Send(sctx, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_SettingsAck{SettingsAck: ack}})
	if err != nil {
		m.log.InfoContext(ctx, "settings.ack_unsent", "err_kind", errKind(err), "generation", ack.GetGeneration())
	}
}

func errKind(err error) string {
	switch {
	case errors.Is(err, conn.ErrNoStream):
		return "no_stream"
	case errors.Is(err, conn.ErrQueueFull):
		return "queue_full"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "ended"
	case errors.Is(err, ring.ErrSettingsOrder):
		return "order"
	}
	return "other"
}

// parent is the context of Settings.trace: the merge is the child of the
// hub's publish (02g "Trace context").
func parent(ctx context.Context, s *agentv1.Settings) context.Context {
	carrier := propagation.MapCarrier{}
	if tp := s.GetTrace().GetTraceparent(); tp != "" {
		carrier["traceparent"] = tp
		carrier["tracestate"] = s.GetTrace().GetTracestate()
	}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}
