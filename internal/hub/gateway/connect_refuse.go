package gateway

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// connectAnswer is how Connect refuses a Hello: a Bye (02g "Connect-level
// failures") or a Connect error (02g "Hello refusals").
type connectAnswer struct {
	bye       agentv1.ByeReason
	code      connect.Code
	reason    typev1.ReasonCode
	retryable bool
	msg       string
}

// Refusal causes: the closed set of connect.refused{cause} and
// fleet_hub_connect_refused_total{cause} (row 5 PR 5a-ii refusal ruling).
const (
	causeNoClientCert = "no_client_cert"       // no verified client certificate
	causeCertNoSAN    = "cert_no_san"          // CN and URI SAN fleet://machine/<CN> do not agree
	causeHelloTimeout = "hello_timeout"        // no message within the Hello wait
	causeNotHello     = "not_hello"            // the first message is not Hello
	causeSecondHello  = "second_hello"         // a Hello on a stream that has one
	causeMachineID    = "machine_id_mismatch"  // Hello.machine_id is not the certificate's
	causeField        = "field_invalid"        // boot_id or install_id malformed
	causeSerialOther  = "serial_other_machine" // the serial is stored for another machine
	causeEpochRange   = "epoch_out_of_range"   // the next epoch would pass statedb.MaxEpoch
	causeRateLimited  = "rate_limited"         // a second Hello for the machine inside HelloEvery
	causeRevoked      = "revoked"              // the machine is REVOKED or its serial is revoked
	causeUnknown      = "unknown_machine"      // no machine row
	causeProto        = "proto_unsupported"    // no common proto version
)

// connectAnswers is the one table of Hello refusals (02g "Hello refusals"
// and "Connect-level failures"). causeField answers FIELD_TOO_LARGE or
// INVALID_ARGUMENT by the field (refuseField).
var connectAnswers = map[string]connectAnswer{
	causeProto:   {bye: agentv1.ByeReason_BYE_REASON_PROTO_UNSUPPORTED},
	causeRevoked: {bye: agentv1.ByeReason_BYE_REASON_REVOKED},
	causeUnknown: {bye: agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE},
	causeNoClientCert: {
		code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		msg: "Connect needs the machine's client certificate",
	},
	causeCertNoSAN: {
		code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		msg: "the client certificate does not name a machine",
	},
	causeHelloTimeout: {code: connect.CodeDeadlineExceeded, retryable: true, msg: "no Hello in time"},
	causeNotHello: {
		code: connect.CodeInvalidArgument, reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		msg: "the first message must be Hello",
	},
	causeSecondHello: {
		code: connect.CodeInvalidArgument, reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		msg: "a stream has one Hello",
	},
	causeMachineID: {
		code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		msg: "Hello.machine_id is not the certificate's machine",
	},
	causeField: {
		code: connect.CodeInvalidArgument, reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		msg: "boot_id and install_id must be 1 to 64 printable ASCII characters without spaces",
	},
	causeSerialOther: {
		code: connect.CodeUnauthenticated, reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		msg: "the hub does not know this certificate",
	},
	causeEpochRange: {
		code: connect.CodeFailedPrecondition, reason: typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH,
		msg: "the machine has no connection epoch left; revoke, delete and enroll it again",
	},
	causeRateLimited: {
		code: connect.CodeResourceExhausted, reason: typev1.ReasonCode_REASON_CODE_RATE_LIMITED,
		retryable: true, msg: "one Hello per machine per 5 s",
	},
}

// helloRefusal names one refused Hello: the cause and, for a rate limit, the
// time left; reason overrides the table's ErrorDetail reason when set.
type helloRefusal struct {
	cause      string
	reason     typev1.ReasonCode
	retryAfter time.Duration
}

// refuse logs a refused Hello as connect.refused{cause, reason, peer,
// machine_id}, counts it in fleet_hub_connect_refused_total{cause} and
// answers it: a Bye on the stream (the handler then returns nil) or the
// Connect error. Nothing was written for it.
func (a *agentService) refuse(ctx context.Context, bs *connectStream, peer, machineID string, r helloRefusal) error {
	ans := connectAnswers[r.cause]
	if r.reason != typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
		ans.reason = r.reason
	}
	a.m.refused.Add(ctx, 1, metric.WithAttributes(attribute.String("cause", r.cause)))
	args := []any{"cause", r.cause, "peer", peer}
	if machineID != "" {
		args = append(args, "machine_id", machineID)
	}
	if ans.bye != agentv1.ByeReason_BYE_REASON_UNSPECIFIED {
		a.log.WarnContext(ctx, "connect.refused", append(args, "reason", byeLabel(ans.bye))...)
		return bs.Send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: &agentv1.Bye{Reason: ans.bye}}})
	}
	d := &typev1.ErrorDetail{Reason: ans.reason, Retryable: ans.retryable}
	if r.retryAfter > 0 {
		d.RetryAfter = durationpb.New(r.retryAfter)
	}
	err := withDetail(connect.NewError(ans.code, errors.New(ans.msg)), d)
	reason := "none"
	if ans.reason != typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
		reason = reasonOf(err)
	}
	a.log.WarnContext(ctx, "connect.refused", append(args, "reason", reason)...)
	return err
}

// connectFailed is the hub's own failure at a Hello, logged as connect.failed{
// step, err_kind, machine_id}: a store failure (step store) is UNAVAILABLE
// with retryable set, the Enroll and RenewCert rule; a Hello the store
// still found malformed after the gateway's checks (step validate) is the
// hub's bug, INTERNAL. A cancelled write has an unknown outcome: a row it
// left live is displaced by the machine's next Hello.
func (a *agentService) connectFailed(ctx context.Context, machineID, step string, err error) error {
	a.log.ErrorContext(ctx, "connect.failed", "step", step, "err_kind", errKind(err), "machine_id", machineID)
	if step == "store" {
		return withDetail(connect.NewError(connect.CodeUnavailable, errors.New("the hub store is busy; retry")),
			&typev1.ErrorDetail{Retryable: true})
	}
	return refusal(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "the hub could not accept the Hello")
}
