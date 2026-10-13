package conn_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// hubRetryError is a Connect error with the hub's ErrorDetail asking for a
// wait before the next try.
func hubRetryError(code connect.Code, reason typev1.ReasonCode, after time.Duration) error {
	err := connect.NewError(code, errFake)
	if d, derr := connect.NewErrorDetail(&typev1.ErrorDetail{
		Reason: reason, Retryable: true, RetryAfter: durationpb.New(after),
	}); derr == nil {
		err.AddDetail(d)
	}
	return err
}

// A hub that rate-limits Hello with retry_after holds the agent off: the
// next try waits out the asked time, not just the backoff, and the failure
// is logged as a refusal, not io.
func TestARateLimitedHelloWaitsOutRetryAfter(t *testing.T) {
	t.Parallel()
	const asked = 1200 * time.Millisecond
	limited := func(int) error {
		return hubRetryError(connect.CodeResourceExhausted,
			typev1.ReasonCode_REASON_CODE_RATE_LIMITED, asked)
	}
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) { h.refuse = limited })
	only := onlyAddress(t, a, h.addr)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		only(o)
		// First/MinGap small so a try without the asked wait is fast; Max
		// above the asked wait so the ceiling does not hide it.
		o.Backoff.First, o.Backoff.Max, o.Backoff.MinGap = 50*time.Millisecond, 5*time.Second, 10*time.Millisecond
	})
	waitFor(t, 20*time.Second, "two failed tries", func() bool { return len(a.events(t, "connect.failed")) >= 2 })
	r.running(t)
	for _, f := range a.events(t, "connect.failed") {
		if f["step"] != "hello" || f["err_kind"] != "refused" {
			t.Errorf("a try failed as %v, want step hello and err_kind refused", f)
		}
		if f["retry_after_ms"] != float64(asked.Milliseconds()) {
			t.Errorf("a try failed as %v, want retry_after_ms %d", f, asked.Milliseconds())
		}
	}
	attempts := a.events(t, "connect.attempt")
	if len(attempts) < 2 {
		t.Fatalf("%d connect.attempt lines, want at least 2", len(attempts))
	}
	const logSkew = 150 * time.Millisecond
	if d := logTime(t, attempts[1]).Sub(logTime(t, attempts[0])); d < asked-logSkew {
		t.Errorf("tries 1 and 2 are %v apart, want at least the asked %v", d, asked)
	}
	if f, ok := r.loop.Status().Failures[h.addr]; !ok || f.Step != "hello" || f.ErrKind != "refused" {
		t.Errorf("the status keeps %+v, want step hello and err_kind refused", f)
	}
	r.cancel()
	if err := r.result(t, 10*time.Second); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
}

// A hub cannot park the agent: an absurd retry_after is capped at the
// backoff ceiling, so tries keep going at the backoff pace.
func TestAnAbsurdRetryAfterIsCappedAtTheBackoffCeiling(t *testing.T) {
	t.Parallel()
	absurd := func(int) error {
		return hubRetryError(connect.CodeResourceExhausted,
			typev1.ReasonCode_REASON_CODE_RATE_LIMITED, 30*time.Second)
	}
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) { h.refuse = absurd })
	only := onlyAddress(t, a, h.addr)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		only(o)
		o.Backoff.First, o.Backoff.Max, o.Backoff.MinGap = 50*time.Millisecond, 100*time.Millisecond, 10*time.Millisecond
	})
	waitFor(t, 20*time.Second, "three failed tries", func() bool { return len(a.events(t, "connect.failed")) >= 3 })
	r.running(t)
	r.cancel()
	if err := r.result(t, 10*time.Second); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
}
