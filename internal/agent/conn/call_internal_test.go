package conn

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
)

// A cut stream (connect-go turns an HTTP/2 RST_STREAM with CANCEL into
// "canceled": the hub stopping, its timer winning, a proxy) can reach the
// agent just before the agent's own cap fires, or just after. Both orders
// must give the same answer: the cap ended the call, the error carries the
// deadline, and a rerun is safe.
func TestTheCapEndingACallIsTheSameWhicheverSideNoticesFirst(t *testing.T) {
	t.Parallel()
	// What the agent sees of a cut stream: a code and text, no deadline cause.
	wire := connect.NewError(connect.CodeCanceled, errors.New("stream error: stream ID 1; CANCEL"))
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Nanosecond, errEnrollCap)
	defer cancel()
	<-ctx.Done()
	again, err := outcome(ctx, wire, new(atomic.Bool), "127.0.0.1:1")
	if again || !errors.Is(err, ErrHubFailed) || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), rerun) {
		t.Fatalf("outcome = %v, %v; want ErrHubFailed with the deadline and the rerun advice, not retried", again, err)
	}
	if got := errKind(err); got != "timeout" {
		t.Errorf("err_kind = %q, want timeout", got)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error %q spans lines", err)
	}
}
