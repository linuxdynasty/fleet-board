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

// importCounters holds one fleet_hub_config_imports_total per Fleet, built
// on first use. The counter lives here and not in NewFleet so this slice
// stays out of the constructor rows the unmerged row-12 slices edit; fold
// it into NewFleet when those merge.
var importCounters sync.Map // *Fleet -> metric.Int64Counter

func (f *Fleet) importCounter() metric.Int64Counter {
	if v, ok := importCounters.Load(f); ok {
		return v.(metric.Int64Counter) //nolint:forcetypeassert // only counters are stored
	}
	c, err := f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_config_imports_total",
		metric.WithDescription("Config imports by outcome: planned, applied, replayed, refused or failed."))
	if err != nil {
		return nil
	}
	actual, _ := importCounters.LoadOrStore(f, c)
	return actual.(metric.Int64Counter) //nolint:forcetypeassert // only counters are stored
}

func (f *Fleet) countImport(ctx context.Context, outcome string) {
	if c := f.importCounter(); c != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// configPlanned counts and logs a dry run: the plan it returned, applied
// false, changing nothing.
func (f *Fleet) configPlanned(ctx context.Context, actor string, lines int, hash string) {
	f.countImport(ctx, "planned")
	f.tel.Logger.InfoContext(ctx, "config.import_planned",
		"lines", lines, "plan_hash", hash, "actor", actor)
}

// configApplied counts and logs an applied import: ok for a committed
// apply, replayed for an idempotent retry. created and updated count
// repos; machines counts relabelled enrolled machines and pending kept
// entries. The audit row carries the full detail; this is the 3 AM signal.
func (f *Fleet) configApplied(ctx context.Context, actor, hash string, created, updated, machines, pending int, replayed bool) {
	outcome := "applied"
	if replayed {
		outcome = "replayed"
	}
	f.countImport(ctx, outcome)
	f.tel.Logger.InfoContext(ctx, "config.import_applied",
		"plan_hash", hash, "created", created, "updated", updated,
		"machines", machines, "pending", pending, "replayed", replayed, "actor", actor)
}

// configRefused counts and logs an import caller error by its reason and
// returns it. Every refusal path of ImportConfig ends here, so operators
// can tell why imports fail.
func (f *Fleet) configRefused(ctx context.Context, actor string, err error) error {
	f.countImport(ctx, "refused")
	f.tel.Logger.InfoContext(ctx, "config.import_refused",
		"reason", errorReason(err), "actor", actor)
	return err
}

// configNotApplied counts and logs an apply that answered a plan with
// applied false: every line the document cannot apply yet. reason is the
// blocking problem (CONFIG_INVALID, or SKIPPED when allow_skipped is off).
func (f *Fleet) configNotApplied(ctx context.Context, actor, reason, hash string, lines int) {
	f.countImport(ctx, "refused")
	f.tel.Logger.InfoContext(ctx, "config.import_refused",
		"reason", reason, "plan_hash", hash, "lines", lines, "actor", actor)
}

// configFailed counts and logs the hub's own import failure at step (a
// closed set: encode, decode, store) and returns INTERNAL without the
// cause's text.
func (f *Fleet) configFailed(ctx context.Context, actor, step string, cause error) error {
	f.countImport(ctx, "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "config.import_failed",
		"step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not apply the import", nil, nil)
}
