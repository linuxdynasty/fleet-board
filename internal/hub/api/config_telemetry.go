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

// Owner config write ops for fleet_hub_config_writes_total.
const (
	opAccountCreate     = "account_create"
	opAccountUpdate     = "account_update"
	opAccountDelete     = "account_delete"
	opIntegrationUpdate = "integration_update"
	opSettingsUpdate    = "settings_update"
	opExport            = "export"
)

// configCounters holds one fleet_hub_config_writes_total per Fleet, built
// on first use. The counter lives here and not in NewFleet so this slice
// stays out of the constructor rows the unmerged row-12 slices edit; fold
// it into NewFleet when those merge.
var configCounters sync.Map // *Fleet -> metric.Int64Counter

func (f *Fleet) configCounter() metric.Int64Counter {
	if v, ok := configCounters.Load(f); ok {
		return v.(metric.Int64Counter) //nolint:forcetypeassert // only counters are stored
	}
	c, err := f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_config_writes_total",
		metric.WithDescription("Owner config writes by op and outcome: ok, replayed, refused or failed."))
	if err != nil {
		return nil
	}
	actual, _ := configCounters.LoadOrStore(f, c)
	return actual.(metric.Int64Counter) //nolint:forcetypeassert // only counters are stored
}

func (f *Fleet) countConfig(ctx context.Context, op, outcome string) {
	if c := f.configCounter(); c != nil {
		c.Add(ctx, 1, metric.WithAttributes(
			attribute.String("op", op), attribute.String("outcome", outcome)))
	}
}

// configWrote counts and logs a config write with its resource and
// version: ok for a committed write, replayed for an idempotent retry.
// The audit row carries the full detail; this is the 3 AM signal. Each
// area logs its own static event (sloglint: static messages only).
func (f *Fleet) configOpWrote(ctx context.Context, op, actor, resource string, version uint64, replayed bool) {
	outcome := "ok"
	if replayed {
		outcome = "replayed"
	}
	f.countConfig(ctx, op, outcome)
	switch configArea(op) {
	case "integration":
		f.tel.Logger.InfoContext(ctx, "config.integration_written",
			"op", op, "resource", resource, "version", version, "replayed", replayed, "actor", actor)
	case "settings":
		f.tel.Logger.InfoContext(ctx, "config.settings_written",
			"op", op, "resource", resource, "version", version, "replayed", replayed, "actor", actor)
	default:
		f.tel.Logger.InfoContext(ctx, "config.account_written",
			"op", op, "resource", resource, "version", version, "replayed", replayed, "actor", actor)
	}
}

// configRefused counts and logs a config caller error by its reason and
// returns it. Every refusal path of the config writes ends here, so
// operators can tell why they fail.
func (f *Fleet) configOpRefused(ctx context.Context, op, actor string, err error) error {
	f.countConfig(ctx, op, "refused")
	reason := errorReason(err)
	switch configArea(op) {
	case "integration":
		f.tel.Logger.InfoContext(ctx, "config.integration_write_refused",
			"reason", reason, "op", op, "actor", actor)
	case "settings":
		f.tel.Logger.InfoContext(ctx, "config.settings_write_refused",
			"reason", reason, "op", op, "actor", actor)
	default:
		f.tel.Logger.InfoContext(ctx, "config.account_write_refused",
			"reason", reason, "op", op, "actor", actor)
	}
	return err
}

// configFailed counts and logs the hub's own config failure at step (a
// closed set: encode, decode, store) and returns INTERNAL without the
// cause's text.
func (f *Fleet) configOpFailed(ctx context.Context, op, verb, actor, step string, cause error) error {
	f.countConfig(ctx, op, "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	switch configArea(op) {
	case "integration":
		f.tel.Logger.ErrorContext(ctx, "config.integration_write_failed",
			"step", step, "err_kind", kind, "op", op, "actor", actor)
	case "settings":
		f.tel.Logger.ErrorContext(ctx, "config.settings_write_failed",
			"step", step, "err_kind", kind, "op", op, "actor", actor)
	default:
		f.tel.Logger.ErrorContext(ctx, "config.account_write_failed",
			"step", step, "err_kind", kind, "op", op, "actor", actor)
	}
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not "+verb+" the configuration", nil, nil)
}

// configWriteError renders a config writer-job failure: caller errors
// stay refused, the hub's own become failed. An already-INTERNAL error (a
// corrupt stored row) is counted failed and kept as is. verb names the op
// for the failure text ("create", "update", "delete", "export").
func (f *Fleet) configOpWriteError(ctx context.Context, op, verb, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() != connect.CodeInternal {
		return f.configOpRefused(ctx, op, actor, err)
	}
	if errors.As(err, &ce) {
		f.countConfig(ctx, op, "failed")
		switch configArea(op) {
		case "integration":
			f.tel.Logger.ErrorContext(ctx, "config.integration_write_failed",
				"step", "store", "err_kind", "io", "op", op, "actor", actor)
		case "settings":
			f.tel.Logger.ErrorContext(ctx, "config.settings_write_failed",
				"step", "store", "err_kind", "io", "op", op, "actor", actor)
		default:
			f.tel.Logger.ErrorContext(ctx, "config.account_write_failed",
				"step", "store", "err_kind", "io", "op", op, "actor", actor)
		}
		return err
	}
	return f.configOpFailed(ctx, op, verb, actor, "store", err)
}

// configArea is the log-event area of an op: account, integration or
// settings.
func configArea(op string) string {
	switch op {
	case opIntegrationUpdate:
		return "integration"
	case opSettingsUpdate:
		return "settings"
	default:
		return "account"
	}
}
