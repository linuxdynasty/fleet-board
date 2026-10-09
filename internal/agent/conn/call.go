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
	// rerun is the advice when the tries ran out: the hub replays a stored
	// answer, so a second run is safe.
	rerun = "nothing was saved; run the same command again"
)

// errEnrollCap is the cause when Options.Timeout ends the enroll, so it is
// told apart from the caller's own deadline or cancel.
var errEnrollCap = errors.New("conn: the enroll ran out of time")

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
			return nil, fmt.Errorf("%w after %d tries; %s: %w", ErrHubFailed, try, rerun, err)
		}
		r.log.InfoContext(ctx, "enroll.retry", "attempt", try, "err_kind", errKind(err))
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w after %d tries; %s: %w", ErrHubFailed, try, rerun, ctx.Err())
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
		if errors.Is(context.Cause(ctx), errEnrollCap) {
			return false, fmt.Errorf("%w: the enroll ran out of time during a call; %s: %w", ErrHubFailed, rerun, err)
		}
		// The caller stopped the run; no advice.
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
		// The same request fails the same way again; fleetd's log says why.
		return false, fmt.Errorf("%w: it failed on its side: %s (%s)", ErrHubFailed, clean(ce.Message(), maxHubMessage), ce.Code())
	}
	// A bare answer with no ErrorDetail: the stream was cut or the hub was
	// not ready. RESOURCE_EXHAUSTED is the 1 MiB request cap (02g); a resend
	// is refused the same way, so it is not tried again.
	switch ce.Code() {
	case connect.CodeUnavailable, connect.CodeUnknown, connect.CodeInternal, connect.CodeDeadlineExceeded,
		connect.CodeAborted:
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

// pinnedTransport dials TLS itself, so that a handshake the peer fails (the
// pin, the name, the key proof, an alert or a non-TLS answer) marks the hub
// untrusted. A connection that is refused, drops, resets or times out is a
// lost connection, retried within the enroll tries.
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

// transportError reports whether a failed handshake lost its connection:
// a read or write on the socket failed, the stream ended, or a deadline or
// cancel cut it. Anything else crypto/tls returns is the peer failing the
// handshake: a chain or name the pin refuses, a bad key proof, the peer's
// alert ("remote error") or a record that is not TLS.
func transportError(err error) bool {
	var rh tls.RecordHeaderError
	var cv *tls.CertificateVerificationError
	if errors.As(err, &rh) || errors.As(err, &cv) {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Op == "read" || op.Op == "write"
	}
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}
