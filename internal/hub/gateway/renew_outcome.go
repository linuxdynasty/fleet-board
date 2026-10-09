package gateway

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// renewOutcome maps a statedb.Renew error: a missing machine row or an
// unknown or revoked peer serial is UNAUTHENTICATED (CREDENTIALS_INVALID), a
// REVOKED machine PERMISSION_DENIED (MACHINE_REVOKED); a signing failure is
// the hub's, and anything else is the store's, UNAVAILABLE and retryable. A
// cancelled write has an unknown outcome; the retry gets the stored
// certificate.
func (d *enrollDeps) renewOutcome(ctx context.Context, machineID, peer string, err error) error {
	switch {
	case errors.Is(err, statedb.ErrMachineUnknown):
		return d.renewRefused(ctx, machineID, peer, "machine_unknown", refusal(connect.CodeUnauthenticated,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "the hub does not know this machine"))
	case errors.Is(err, statedb.ErrMachineRevoked):
		return d.renewRefused(ctx, machineID, peer, "machine_revoked", refusal(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_MACHINE_REVOKED, "the machine is revoked"))
	case errors.Is(err, statedb.ErrPeerSerialUnknown):
		return d.renewRefused(ctx, machineID, peer, "serial_unknown", refusal(connect.CodeUnauthenticated,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "the hub does not know this certificate"))
	case errors.Is(err, errSign):
		return d.renewFailed(ctx, machineID, "sign", err)
	default:
		return d.renewFailed(ctx, machineID, "store", err)
	}
}

// renewRefused counts and logs the caller's error with its reason, a cause
// from a closed set (no_client_cert, csr_invalid, csr_same_key,
// machine_unknown, machine_revoked, serial_unknown) and the peer's address,
// and returns it. machineID is empty when no client certificate was given.
func (d *enrollDeps) renewRefused(ctx context.Context, machineID, peer, cause string, err *connect.Error) error {
	d.renewals.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "refused")))
	args := []any{"result", "refused", "reason", reasonOf(err), "cause", cause, "peer", peer}
	if machineID != "" {
		args = append(args, "machine_id", machineID)
	}
	d.log.WarnContext(ctx, "cert.renew_refused", args...)
	return err
}

// renewFailed counts and logs the hub's own failure at step (store, sign,
// encode). A store failure is UNAVAILABLE with retryable set and no
// reason; the others are INTERNAL. The cause's text is never sent or logged.
func (d *enrollDeps) renewFailed(ctx context.Context, machineID, step string, cause error) error {
	d.renewals.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "failed")))
	d.log.ErrorContext(ctx, "cert.renew_refused", "result", "failed", "step", step, "err_kind", errKind(cause),
		"machine_id", machineID)
	if step == "store" {
		return withDetail(connect.NewError(connect.CodeUnavailable, errors.New("the hub store is busy; retry")),
			&typev1.ErrorDetail{Retryable: true})
	}
	return refusal(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "the hub could not renew the certificate")
}

// renewed counts and logs a successful RenewCert, new or replayed.
func (d *enrollDeps) renewed(ctx context.Context, machineID string, r statedb.Renewed, notAfter time.Time) {
	result := "renewed"
	if r.Replayed {
		result = "replayed"
	}
	d.renewals.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	d.log.InfoContext(ctx, "cert.renewed", "machine_id", machineID, "result", result, "serial", r.Serial,
		"not_after", notAfter.UTC().Format(time.RFC3339))
}
