package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// ErrNoHubFound means enroll had no hub address, browsed (mDNS), and found
// no hub that holds the token's CA. Nothing was sent.
var ErrNoHubFound = errors.New("conn: no hub found on the local network")

// noHubError is [ErrNoHubFound] with what the browse saw and its 05b
// err_kind: io when the browse failed, timeout when no fleetd answered,
// refused when every answer failed the handshake.
type noHubError struct {
	kind, why string
}

func (e *noHubError) Error() string {
	return fmt.Sprintf("%v (%s); nothing was sent. Pass --hub host[:port]. "+
		"A firewall on this machine that drops direct UDP replies to mDNS queries (port 5353), "+
		"as ufw does on Linux by default, hides the hub from discovery", ErrNoHubFound, e.why)
}

func (e *noHubError) Is(target error) bool { return target == ErrNoHubFound }

// findHub browses once (03 A2) and returns the first answer whose TLS
// handshake proves it holds the token's CA (02g A1 step 2), so the token
// goes only to the hub it names. Each answer gets the handshake and
// nothing else; one that fails it or cannot be reached is skipped.
func findHub(ctx context.Context, r *enrollRun, opts Options, tok Token) (candidate, error) {
	ctx, span := opts.Telemetry.Tracer(scope).Start(ctx, "agent.discover")
	defer span.End()
	var st BrowseStatus
	b := startBrowse(ctx, opts.Discover, opts.BrowseTimeout, func(s BrowseStatus, _ time.Duration) { st = s })
	tried := 0
	for c := range b.cands {
		tried++
		if err := probeHub(ctx, opts, tok, c); err != nil {
			continue
		}
		b.stop() // st is final once the browse's goroutine is done
		span.SetAttributes(attribute.String("result", "found"), attribute.Int("kept", st.Kept))
		r.log.InfoContext(ctx, "enroll.discovered", "peer", c.addr, "kept", st.Kept, "dropped", st.Dropped,
			"elapsed_ms", st.Elapsed.Milliseconds())
		return c, nil
	}
	b.stop()
	err := &noHubError{kind: "timeout", why: fmt.Sprintf("no fleetd answered within %v", st.Elapsed.Round(time.Millisecond))}
	switch {
	case st.ErrKind != "":
		err = &noHubError{kind: "io", why: "the mDNS browse could not run on this machine's network"}
	case tried > 0:
		err = &noHubError{kind: "refused", why: fmt.Sprintf("%d answered, none proved it holds this token's CA", tried)}
	}
	span.SetAttributes(attribute.String("result", "none"), attribute.Int("kept", st.Kept))
	span.SetStatus(codes.Error, err.kind)
	return candidate{}, err
}

// probeHub runs only the pinned TLS handshake with c: no request follows.
func probeHub(ctx context.Context, opts Options, tok Token, c candidate) error {
	ctx, span := opts.Telemetry.Tracer(scope).Start(ctx, "agent.discover.candidate")
	defer span.End()
	span.SetAttributes(attribute.String("peer", c.addr), attribute.String("source", c.source))
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return err
	}
	d := &net.Dialer{Timeout: handshakeTimeout}
	raw, err := d.DialContext(ctx, "tcp", c.dial)
	if err == nil {
		hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
		err = tls.Client(raw, pinnedTLS(tok.CAFingerprint, host)).HandshakeContext(hctx)
		cancel()
		_ = raw.Close()
	}
	if err != nil {
		span.SetAttributes(attribute.String("result", "failed"))
		span.SetStatus(codes.Error, "handshake")
		return err
	}
	span.SetAttributes(attribute.String("result", "passed"))
	return nil
}
