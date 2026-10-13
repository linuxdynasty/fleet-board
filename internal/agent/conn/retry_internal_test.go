package conn

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// A hub Hello refusal is a refusal, whatever code it carries: the hub said
// no (02g "Hello refusals"). Before, only Unauthenticated, PermissionDenied
// and Unimplemented mapped to refused; InvalidArgument, FailedPrecondition
// and ResourceExhausted fell through to io.
func TestConnErrKindMapsHubHelloRefusals(t *testing.T) {
	t.Parallel()
	refused := []connect.Code{
		connect.CodeUnauthenticated,
		connect.CodePermissionDenied,
		connect.CodeUnimplemented,
		connect.CodeInvalidArgument,
		connect.CodeFailedPrecondition,
		connect.CodeResourceExhausted,
	}
	for _, code := range refused {
		if got := connErrKind(connect.NewError(code, errors.New("no")), false); got != "refused" {
			t.Errorf("connErrKind(%v) = %q, want refused", code, got)
		}
	}
	if got := connErrKind(connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), false); got != "timeout" {
		t.Errorf("connErrKind(DeadlineExceeded) = %q, want timeout", got)
	}
}

// retryAfter is the hub's asked wait from a Connect error's ErrorDetail.
func TestRetryAfterFromHubError(t *testing.T) {
	t.Parallel()
	withWait := connect.NewError(connect.CodeResourceExhausted, errors.New("slow down"))
	d, err := connect.NewErrorDetail(&typev1.ErrorDetail{
		Reason: typev1.ReasonCode_REASON_CODE_RATE_LIMITED, Retryable: true, RetryAfter: durationpb.New(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	withWait.AddDetail(d)
	if got := retryAfter(withWait); got != 3*time.Second {
		t.Errorf("retryAfter(rate limited, 3s) = %v, want 3s", got)
	}
	bare := connect.NewError(connect.CodeUnavailable, errors.New("busy"))
	if got := retryAfter(bare); got != 0 {
		t.Errorf("retryAfter(no detail) = %v, want 0", got)
	}
	negative := connect.NewError(connect.CodeResourceExhausted, errors.New("slow down"))
	nd, err := connect.NewErrorDetail(&typev1.ErrorDetail{RetryAfter: durationpb.New(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	negative.AddDetail(nd)
	if got := retryAfter(negative); got != 0 {
		t.Errorf("retryAfter(negative) = %v, want 0", got)
	}
	if got := retryAfter(errors.New("not a connect error")); got != 0 {
		t.Errorf("retryAfter(non-connect) = %v, want 0", got)
	}
}
