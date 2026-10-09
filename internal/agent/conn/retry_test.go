package conn_test

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

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
	cases := []struct {
		name    string
		fail    func(call int) error
		calls   int
		ok      bool
		retries int
	}{
		{"busy twice, then enrolled", func(call int) error {
			if call <= 2 {
				return busy
			}
			return nil
		}, 3, true, 2},
		{"a stream reset, then enrolled", func(call int) error {
			if call == 1 {
				panic(http.ErrAbortHandler)
			}
			return nil
		}, 2, true, 1},
		{"busy on every try", func(int) error { return busy }, conn.DefaultAttempts, false, conn.DefaultAttempts - 1},
		{"the hub's own failure is not retried", func(int) error { return internal }, 1, false, 0},
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
			if lines := a.events(t, "enroll.failed"); len(lines) != 1 || lines[0]["step"] != "call" {
				t.Errorf("enroll.failed lines %v, want one with step=call", lines)
			}
			if got := perm(t, filepath.Join(a.dir, conn.PendingKeyFile)); got != 0o600 {
				t.Errorf("the pending key is mode %04o, want it kept at 0600 for the next run", got)
			}
		})
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
