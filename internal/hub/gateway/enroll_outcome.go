package gateway

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/auditcap"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// enrollDeps is what Enroll and RenewCert need, built once by NewServer.
type enrollDeps struct {
	store    *store.Store
	auth     *ca.Authority
	id       statedb.Identity
	caHash   [32]byte
	log      *slog.Logger
	now      func() time.Time
	random   io.Reader
	enrolls  metric.Int64Counter
	renewals metric.Int64Counter
	// audit caps the machine.enroll_failed rows; auditRows counts them.
	audit     *auditcap.Cap
	auditRows metric.Int64Counter
}

func newEnrollDeps(opts ServerOptions) (*enrollDeps, error) {
	d := &enrollDeps{
		store: opts.Store, auth: opts.Certs.auth, id: opts.Identity, caHash: caHash(opts.Identity.CACert),
		log: opts.Telemetry.Logger.With("component", "gateway"), now: opts.Now, random: opts.Random,
		audit: auditcap.New(),
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.random == nil {
		d.random = rand.Reader
	}
	var err error
	d.enrolls, err = opts.Telemetry.Meter(scope).Int64Counter(opts.Telemetry.MetricPrefix()+"_enroll_total",
		metric.WithDescription("Enroll calls, by result: enrolled, replayed, refused (the caller's error) or failed (the hub's)."))
	if err != nil {
		return nil, fmt.Errorf("gateway: enroll counter: %w", err)
	}
	d.renewals, err = opts.Telemetry.Meter(scope).Int64Counter(opts.Telemetry.MetricPrefix()+"_agent_cert_renewals_total",
		metric.WithDescription("RenewCert calls, by result: renewed, replayed, refused (the caller's error) or failed (the hub's)."))
	if err != nil {
		return nil, fmt.Errorf("gateway: renewal counter: %w", err)
	}
	d.auditRows, err = opts.Telemetry.Meter(scope).Int64Counter(opts.Telemetry.MetricPrefix()+"_enroll_audit_rows_total",
		metric.WithDescription("Refused Enrolls by audit outcome: written (own machine.enroll_failed row), suppressed (capped), "+
			"summary (a minute's summary row) or write_failed (rows the store did not take)."))
	if err != nil {
		return nil, fmt.Errorf("gateway: enroll audit counter: %w", err)
	}
	return d, nil
}

// refusal is a Connect error with the ErrorDetail reason of 02g. Its text
// never echoes the request.
func refusal(code connect.Code, reason typev1.ReasonCode, msg string) *connect.Error {
	return withDetail(connect.NewError(code, errors.New(msg)), &typev1.ErrorDetail{Reason: reason})
}

func withDetail(err *connect.Error, d *typev1.ErrorDetail) *connect.Error {
	if detail, derr := connect.NewErrorDetail(d); derr == nil {
		err.AddDetail(detail)
	}
	return err
}

// refused counts and logs the caller's error by its reason and the peer's
// address, writes its audit row within the caps, and returns it.
func (d *enrollDeps) refused(ctx context.Context, peer string, err *connect.Error) error {
	d.enrolls.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "refused")))
	d.log.InfoContext(ctx, "enroll.refused", "reason", reasonOf(err), "peer", peer)
	d.auditRefusal(ctx, peer, err)
	return err
}

// reasonOf is the ErrorDetail reason of err without REASON_CODE_.
func reasonOf(err *connect.Error) string {
	reason := "UNSPECIFIED"
	for _, det := range err.Details() {
		if v, derr := det.Value(); derr == nil {
			if ed, ok := v.(*typev1.ErrorDetail); ok {
				reason = strings.TrimPrefix(ed.GetReason().String(), "REASON_CODE_")
			}
		}
	}
	return reason
}

// failed counts and logs the hub's own failure at step (a closed set:
// store, previous_machine, random, sign, encode). A store failure is
// UNAVAILABLE with retryable set and no reason (02g); the others are
// INTERNAL. The cause's text is
// never sent or logged.
func (d *enrollDeps) failed(ctx context.Context, step string, cause error) error {
	kind := "io"
	switch {
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		kind = "timeout"
	case errors.Is(cause, statedb.ErrPreviousMachine):
		kind = "refused"
	}
	d.enrolls.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "failed")))
	d.log.ErrorContext(ctx, "enroll.failed", "step", step, "err_kind", kind)
	if step == "store" {
		return withDetail(connect.NewError(connect.CodeUnavailable, errors.New("the hub store is busy; retry")),
			&typev1.ErrorDetail{Retryable: true})
	}
	return refusal(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "the hub could not enroll the machine")
}

// completed counts and logs a successful Enroll, new or replayed.
func (d *enrollDeps) completed(ctx context.Context, e statedb.Enrolled) {
	result := "enrolled"
	if e.Replayed {
		result = "replayed"
	}
	d.enrolls.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	d.log.InfoContext(ctx, "enroll.completed", "machine_id", e.MachineID, "replayed", e.Replayed,
		"hostname_mismatch", e.HostnameMismatch)
}
