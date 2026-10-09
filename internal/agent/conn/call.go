package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Tries of one enroll (defaults.md "Enroll tries"): a busy hub or a lost
// stream is tried again with the same request, which the hub replays (02g).
const (
	defaultFirstWait = 500 * time.Millisecond
	defaultMaxWait   = 4 * time.Second
	callTimeout      = 20 * time.Second
	handshakeTimeout = 10 * time.Second
)

func (r Retry) withDefaults() Retry {
	if r.Attempts <= 0 {
		r.Attempts = DefaultAttempts
	}
	if r.FirstWait <= 0 {
		r.FirstWait = defaultFirstWait
	}
	if r.MaxWait <= 0 {
		r.MaxWait = defaultMaxWait
	}
	return r
}

// wait is full jitter: a random time up to FirstWait doubled per earlier
// try, capped at MaxWait.
func (r Retry) wait(try int) time.Duration {
	ceil := r.MaxWait
	if try < 16 && r.FirstWait<<(try-1) < ceil {
		ceil = r.FirstWait << (try - 1)
	}
	return rand.N(ceil + 1) //nolint:gosec // G404: jitter spreads retries; it is not a secret
}

// call sends req over TLS pinned to the token's CA, trying again on a
// retryable answer or a transport error, up to the policy. The request is
// the same on every try (token, CSR key, install_id), so a try after a lost
// reply gets the hub's stored answer.
func call(ctx context.Context, r *enrollRun, opts Options, addr string, tok Token, req *agentv1.EnrollRequest,
) (*agentv1.EnrollResponse, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrHubAddress, addr)
	}
	var untrusted atomic.Bool
	tr := pinnedTransport(tok.CAFingerprint, host, &untrusted)
	defer tr.CloseIdleConnections()
	client := agentv1connect.NewAgentServiceClient(&http.Client{Transport: tr, Timeout: callTimeout}, "https://"+addr,
		connect.WithGRPC(), connect.WithReadMaxBytes(maxMessageBytes), connect.WithSendMaxBytes(maxMessageBytes))
	policy := opts.Retry.withDefaults()
	for try := 1; ; try++ {
		res, err := client.Enroll(ctx, connect.NewRequest(req))
		if err == nil {
			return res.Msg, nil
		}
		again, err := outcome(ctx, err, &untrusted, addr)
		if !again {
			return nil, err
		}
		if try >= policy.Attempts {
			return nil, fmt.Errorf("%w (%d tries): %w", ErrHubFailed, try, err)
		}
		r.log.InfoContext(ctx, "enroll.retry", "attempt", try, "err_kind", errKind(err))
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (%d tries): %w", ErrHubFailed, try, ctx.Err())
		case <-time.After(policy.wait(try)):
		}
	}
}

// outcome sorts a failed try: an untrusted hub and a refusal stop; a
// retryable answer or a transport error may be tried again; any other
// answer is the hub's own failure.
func outcome(ctx context.Context, err error, untrusted *atomic.Bool, addr string) (bool, error) {
	if untrusted.Load() {
		return false, fmt.Errorf("%w (%s): %w", ErrHubUntrusted, addr, err)
	}
	if ctx.Err() != nil {
		return false, fmt.Errorf("conn: enroll call to %s: %w", addr, err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return true, fmt.Errorf("conn: enroll call to %s: %w", addr, err)
	}
	if ed := errorDetail(ce); ed != nil {
		switch reason := ed.GetReason(); {
		case ed.GetRetryable():
			return true, fmt.Errorf("conn: the hub at %s asked for a retry: %w", addr, err)
		case reason != typev1.ReasonCode_REASON_CODE_UNSPECIFIED && reason != typev1.ReasonCode_REASON_CODE_INTERNAL:
			return false, &RefusedError{Code: ce.Code(), Reason: reason, Message: clean(ce.Message(), maxHubMessage)}
		}
		return false, fmt.Errorf("%w: %s (%s)", ErrHubFailed, clean(ce.Message(), maxHubMessage), ce.Code())
	}
	switch ce.Code() {
	case connect.CodeUnavailable, connect.CodeUnknown, connect.CodeInternal, connect.CodeDeadlineExceeded,
		connect.CodeAborted, connect.CodeResourceExhausted:
		return true, fmt.Errorf("conn: enroll call to %s: %w", addr, err)
	default:
		return false, fmt.Errorf("%w: %w", ErrHubFailed, err)
	}
}

func errorDetail(ce *connect.Error) *typev1.ErrorDetail {
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if ed, ok := v.(*typev1.ErrorDetail); ok {
				return ed
			}
		}
	}
	return nil
}

// pinnedTransport dials TLS itself, so that any failed handshake with a
// peer that answered (the pin, a chain for another name, a certificate the
// peer holds no key for, a non-TLS answer) marks the hub untrusted. A
// connection that is refused, reset or times out is a transport error.
func pinnedTransport(fp [32]byte, host string, untrusted *atomic.Bool) *http.Transport {
	cfg := pinnedTLS(fp, host)
	d := &net.Dialer{Timeout: handshakeTimeout}
	return &http.Transport{
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 64 << 10,
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			raw, err := d.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			c := tls.Client(raw, cfg)
			hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
			defer cancel()
			if err := c.HandshakeContext(hctx); err != nil {
				_ = raw.Close()
				if !transportError(err) {
					untrusted.Store(true)
				}
				return nil, err
			}
			return c, nil
		},
	}
}

// transportError is a failure of the connection, not of the peer's proof.
func transportError(err error) bool {
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		(errors.As(err, &ne) && ne.Timeout())
}
