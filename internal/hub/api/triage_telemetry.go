package api

import (
	"context"
	"errors"
	"sync"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Hub-state triage write ops for fleet_hub_triage_writes_total.
const (
	opTriageSnooze  = "triage_snooze"
	opTriageDismiss = "triage_dismiss"
)

// triageCounters holds one fleet_hub_triage_writes_total per Fleet, built
// on first use. The counter lives here and not in NewFleet so this slice
// stays out of the constructor rows the unmerged row-12 slices edit; fold
// it into NewFleet when those merge.
var triageCounters sync.Map // *Fleet -> metric.Int64Counter

func (f *Fleet) triageCounter() metric.Int64Counter {
	if v, ok := triageCounters.Load(f); ok {
		return v.(metric.Int64Counter) //nolint:forcetypeassert // only counters are stored
	}
	c, err := f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_triage_writes_total",
		metric.WithDescription("Hub-state triage writes by op and outcome: ok, replayed, refused or failed."))
	if err != nil {
		return nil
	}
	actual, _ := triageCounters.LoadOrStore(f, c)
	return actual.(metric.Int64Counter) //nolint:forcetypeassert // only counters are stored
}

func (f *Fleet) countTriage(ctx context.Context, op, outcome string) {
	if c := f.triageCounter(); c != nil {
		c.Add(ctx, 1, metric.WithAttributes(
			attribute.String("op", op), attribute.String("outcome", outcome)))
	}
}

// triageWrote counts and logs a triage write with its item and version:
// ok for a committed write, replayed for an idempotent retry. The audit
// row carries the full detail; this is the 3 AM signal. Each op logs its
// own static event (sloglint: static messages only).
func (f *Fleet) triageWrote(ctx context.Context, op, actor, item string, version uint64, replayed bool) {
	outcome := "ok"
	if replayed {
		outcome = "replayed"
	}
	f.countTriage(ctx, op, outcome)
	switch op {
	case opTriageDismiss:
		f.tel.Logger.InfoContext(ctx, "triage.dismissed",
			"op", op, "item", item, "version", version, "replayed", replayed, "actor", actor)
	default:
		f.tel.Logger.InfoContext(ctx, "triage.snoozed",
			"op", op, "item", item, "version", version, "replayed", replayed, "actor", actor)
	}
}

// triageRefused counts and logs a triage caller error by its reason and
// returns it. Every refusal path of the triage writes ends here, so
// operators can tell why they fail.
func (f *Fleet) triageRefused(ctx context.Context, op, actor string, err error) error {
	f.countTriage(ctx, op, "refused")
	f.tel.Logger.InfoContext(ctx, "triage.write_refused",
		"reason", errorReason(err), "op", op, "actor", actor)
	return err
}

// triageFailed counts and logs the hub's own triage failure at step (a
// closed set: encode, decode, store) and returns INTERNAL without the
// cause's text.
func (f *Fleet) triageFailed(ctx context.Context, op, verb, actor, step string, cause error) error {
	f.countTriage(ctx, op, "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "triage.write_failed",
		"step", step, "err_kind", kind, "op", op, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not "+verb+" the triage item", nil, nil)
}

// triageWriteError renders a triage writer-job failure: caller errors
// stay refused, the hub's own become failed. An already-INTERNAL error (a
// stored row the hub cannot decode) is counted failed and kept as is.
func (f *Fleet) triageWriteError(ctx context.Context, op, verb, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() != connect.CodeInternal {
		return f.triageRefused(ctx, op, actor, err)
	}
	if errors.As(err, &ce) {
		f.countTriage(ctx, op, "failed")
		f.tel.Logger.ErrorContext(ctx, "triage.write_failed",
			"step", "store", "err_kind", "io", "op", op, "actor", actor)
		return err
	}
	return f.triageFailed(ctx, op, verb, actor, "store", err)
}
