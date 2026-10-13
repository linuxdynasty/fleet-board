package conn

import (
	"errors"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Hub refusal classification (02g "Hello refusals"): how a failed try is
// logged (05b err_kind), and the wait the hub asked for before the next
// try. The loop's backoff honors that wait, capped at its own ceiling.

// connErrKind is the 05b err_kind of a connect failure: a peer that failed
// the handshake or refused the machine is "refused", whatever Connect code
// the refusal carries (Unauthenticated, InvalidArgument,
// FailedPrecondition and ResourceExhausted alike).
func connErrKind(err error, untrusted bool) string {
	var ce *connect.Error
	switch {
	case untrusted:
		return "refused"
	case errors.Is(err, errEpochRegressed), errors.Is(err, errSnapshotOversize), errors.Is(err, errProtoVersion),
		errors.Is(err, ring.ErrOutOfRange):
		return "constraint"
	case errors.Is(err, errNotHelloAck):
		return "parse"
	case errors.As(err, &ce) && (ce.Code() == connect.CodeUnauthenticated ||
		ce.Code() == connect.CodePermissionDenied || ce.Code() == connect.CodeUnimplemented ||
		ce.Code() == connect.CodeInvalidArgument || ce.Code() == connect.CodeFailedPrecondition ||
		ce.Code() == connect.CodeResourceExhausted):
		return "refused"
	case errors.As(err, &ce) && ce.Code() == connect.CodeDeadlineExceeded:
		return "timeout"
	}
	return errKind(err)
}

// retryAfter is the wait the hub asked for in err's ErrorDetail, or 0 when
// it asked for none: a missing, undecodable or non-positive retry_after is
// no wait. The caller caps it, so a hub cannot park the agent past its
// backoff ceiling.
func retryAfter(err error) time.Duration {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return 0
	}
	ra := errorDetail(ce).GetRetryAfter()
	if ra == nil {
		return 0
	}
	if d := ra.AsDuration(); d > 0 {
		return d
	}
	return 0
}
