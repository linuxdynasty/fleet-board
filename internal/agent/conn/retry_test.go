package conn_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// 03 W1: a hub that is busy (UNAVAILABLE, retryable) or a lost stream is
// retried with the same token, key and install_id, so the hub replays;
// only a non-retryable reason is a refusal.
func TestEnrollRetriesWhatTheHubSaysToRetry(t *testing.T) {
	t.Parallel()
	busy := hubError(connect.CodeUnavailable, typev1.ReasonCode_REASON_CODE_UNSPECIFIED, true)
	internal := hubError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, false)
	// 02g: a request over the 1 MiB cap is refused the same way every time.
	tooLarge := connect.NewError(connect.CodeResourceExhausted, errFake)
	cases := []struct {
		name    string
		fail    func(call int) error
		calls   int
		ok      bool
		retries int
		// rerun: the error says running the same command again is safe.
		rerun bool
	}{
		{"busy twice, then enrolled", func(call int) error {
			if call <= 2 {
				return busy
			}
			return nil
		}, 3, true, 2, false},
		{"a stream reset, then enrolled", func(call int) error {
			if call == 1 {
				panic(http.ErrAbortHandler)
			}
			return nil
		}, 2, true, 1, false},
		{"busy on every try", func(int) error { return busy }, conn.DefaultAttempts, false, conn.DefaultAttempts - 1, true},
		{"the hub's own failure is not retried", func(int) error { return internal }, 1, false, 0, false},
		{"a request too large is not retried", func(int) error { return tooLarge }, 1, false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeHub{auth: newCA(t), fail: tc.fail}
			addr, tok := startFakeHub(t, f)
			a := newAgent(t)
			_, err := a.enroll(t, addr, tok)
			reqs := f.requests()
			if len(reqs) != tc.calls {
				t.Errorf("the hub got %d calls, want %d", len(reqs), tc.calls)
			}
			for i, r := range reqs[1:] {
				if r.GetToken() != reqs[0].GetToken() || string(r.GetCsrPem()) != string(reqs[0].GetCsrPem()) ||
					r.GetInstallId() != reqs[0].GetInstallId() {
					t.Errorf("try %d changed the token, CSR or install_id", i+2)
				}
			}
			if got := len(a.events(t, "enroll.retry")); got != tc.retries {
				t.Errorf("%d enroll.retry lines, want %d", got, tc.retries)
			}
			if lines := a.events(t, "enroll.refused"); len(lines) != 0 {
				t.Errorf("enroll.refused lines %v; a hub failure is not a refusal", lines)
			}
			if tc.ok {
				if err != nil {
					t.Fatalf("Enroll = %v, want enrolled after retries", err)
				}
				return
			}
			var refused *conn.RefusedError
			if !errors.Is(err, conn.ErrHubFailed) || errors.As(err, &refused) {
				t.Fatalf("Enroll = %v, want ErrHubFailed and no refusal", err)
			}
			if got := strings.Contains(err.Error(), "run the same command again"); got != tc.rerun {
				t.Errorf("Enroll = %q; advises a rerun: %v, want %v", err, got, tc.rerun)
			}
			if lines := a.events(t, "enroll.failed"); len(lines) != 1 || lines[0]["step"] != "call" {
				t.Errorf("enroll.failed lines %v, want one with step=call", lines)
			}
			if got := perm(t, filepath.Join(a.dir, conn.PendingKeyFile)); got != 0o600 {
				t.Errorf("the pending key is mode %04o, want it kept at 0600 for the next run", got)
			}
		})
	}
}

// alwaysBusy is a fake hub that answers every call with a retryable busy,
// and closes first when the first call has been answered.
func alwaysBusy(t *testing.T) (f *fakeHub, addr, tok string, first <-chan struct{}) {
	t.Helper()
	ch := make(chan struct{})
	f = &fakeHub{auth: newCA(t), fail: func(call int) error {
		if call == 1 {
			defer close(ch)
		}
		return hubError(connect.CodeUnavailable, typev1.ReasonCode_REASON_CODE_UNSPECIFIED, true)
	}}
	addr, tok = startFakeHub(t, f)
	return f, addr, tok, ch
}

// Options.Timeout caps the whole enroll: with minute-long waits between
// tries, the run still ends at the cap.
func TestTheEnrollTimeoutCapsTheWholeRun(t *testing.T) {
	t.Parallel()
	f, addr, tok, _ := alwaysBusy(t)
	a := newAgent(t)
	const limit = 2 * time.Second
	start := time.Now()
	_, err := a.enrollWith(t, addr, tok, func(o *conn.Options) {
		o.Timeout = limit
		o.Retry = conn.Retry{FirstWait: time.Minute, MaxWait: time.Minute}
	})
	if took := time.Since(start); took > limit+time.Second {
		t.Errorf("Enroll took %v, want about the %v cap", took, limit)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Enroll = %v, want the deadline", err)
	}
	if n := len(f.requests()); n >= conn.DefaultAttempts {
		t.Errorf("the hub got %d calls, want fewer than %d", n, conn.DefaultAttempts)
	}
	if lines := a.events(t, "enroll.failed"); len(lines) != 1 || lines[0]["err_kind"] != "timeout" {
		t.Errorf("enroll.failed lines %v, want one with err_kind=timeout", lines)
	}
}

// A cancel during the wait between tries ends the enroll at once.
func TestACancelEndsTheWaitBetweenTries(t *testing.T) {
	t.Parallel()
	f, addr, tok, first := alwaysBusy(t)
	a := newAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := conn.Enroll(ctx, conn.Options{
			Dir: a.dir, Hub: addr, Token: tok, Version: "test", Telemetry: a.tel,
			Retry: conn.Retry{FirstWait: time.Minute, MaxWait: time.Minute},
		})
		done <- err
	}()
	select {
	case <-first:
	case <-time.After(15 * time.Second):
		t.Fatal("the hub got no call")
	}
	time.Sleep(100 * time.Millisecond) // the busy answer reaches the agent, which waits
	cancelled := time.Now()
	cancel()
	select {
	case err := <-done:
		if took := time.Since(cancelled); took > 500*time.Millisecond {
			t.Errorf("Enroll returned %v after the cancel, want at once", took)
		}
		if !errors.Is(err, conn.ErrHubFailed) || !errors.Is(err, context.Canceled) {
			t.Errorf("Enroll = %v, want ErrHubFailed with the cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Enroll kept waiting after the cancel")
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("the hub got %d calls, want 1", n)
	}
	if got := len(a.events(t, "enroll.retry")); got != 1 {
		t.Errorf("%d enroll.retry lines, want 1: the cancel came during the first wait", got)
	}
}

// A non-retryable caller error is a refusal on the first answer.
func TestARefusalIsNotRetried(t *testing.T) {
	t.Parallel()
	f := &fakeHub{auth: newCA(t), fail: func(int) error {
		return hubError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_ENROLL_CSR_INVALID, false)
	}}
	addr, tok := startFakeHub(t, f)
	a := newAgent(t)
	_, err := a.enroll(t, addr, tok)
	var refused *conn.RefusedError
	if !errors.As(err, &refused) || refused.Reason != typev1.ReasonCode_REASON_CODE_ENROLL_CSR_INVALID {
		t.Fatalf("Enroll = %v, want a refusal ENROLL_CSR_INVALID", err)
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("the hub got %d calls, want 1", n)
	}
}
