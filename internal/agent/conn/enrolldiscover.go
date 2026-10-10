package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// ErrNoHubFound means enroll had no hub address, browsed (mDNS), and found
// no hub that holds the token's CA. Nothing was sent.
var ErrNoHubFound = errors.New("conn: no hub found on the local network")

// noHubError is [ErrNoHubFound] with what the search saw and its 05b
// err_kind. firewall adds the likely cause when nothing answered.
type noHubError struct {
	kind, why string
	firewall  bool
}

func (e *noHubError) Error() string {
	msg := fmt.Sprintf("%v (%s); nothing was sent. Pass --hub host[:port]", ErrNoHubFound, e.why)
	if e.firewall {
		msg += ". A firewall on this machine that drops direct UDP replies to mDNS queries (port 5353), " +
			"as ufw does on Linux by default, hides the hub from discovery"
	}
	return msg
}

func (e *noHubError) Is(target error) bool { return target == ErrNoHubFound }

// probes counts how the answers tried failed.
type probes struct {
	// untrusted failed the handshake: not this token's hub. lost could not
	// be reached (refused counts the refused connections among them).
	untrusted, lost, refused int
}

func (p *probes) add(err error) {
	switch {
	case !transportError(err) && !isDialError(err):
		p.untrusted++
	case errors.Is(err, syscall.ECONNREFUSED):
		p.lost++
		p.refused++
	default:
		p.lost++
	}
}

func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// failure is the error for a search that found no hub.
func (p probes) failure(st BrowseStatus) *noHubError {
	switch {
	case p.untrusted > 0 && p.lost > 0:
		return &noHubError{kind: "refused", why: fmt.Sprintf("%d answered: %d could not be reached, %d did not prove "+
			"it holds this token's CA", p.untrusted+p.lost, p.lost, p.untrusted)}
	case p.untrusted > 0:
		return &noHubError{kind: "refused", why: fmt.Sprintf("%d answered, none proved it holds this token's CA "+
			"(another fleet's hub, or not a hub)", p.untrusted)}
	case p.lost > 0:
		kind := "timeout"
		if p.refused == p.lost {
			kind = "refused"
		}
		return &noHubError{kind: kind, why: fmt.Sprintf("%d answered but could not be reached", p.lost)}
	case st.ErrKind != "":
		return &noHubError{kind: "io", why: "the mDNS browse could not run on this machine's network", firewall: true}
	}
	return &noHubError{kind: "timeout", why: fmt.Sprintf("no fleetd answered within %v",
		st.Elapsed.Round(time.Millisecond)), firewall: true}
}

// findHub browses once (03 A2) and returns the first answer whose TLS
// handshake proves it holds the token's CA (02g A1 step 2), so the token
// goes only to the hub it names. Each answer gets the handshake and
// nothing else, within its own budget, and the search as a whole is
// capped; an answer that fails or cannot be reached is skipped.
func findHub(ctx context.Context, r *enrollRun, opts Options, tok Token) (candidate, error) {
	ctx, span := opts.Telemetry.Tracer(scope).Start(ctx, "agent.discover")
	defer span.End()
	lim := opts.mdns.withDefaults()
	end := time.Now().Add(lim.phase)
	var st BrowseStatus
	b := startBrowse(ctx, opts.Discover, opts.BrowseTimeout, lim, func(s BrowseStatus, _ time.Duration) { st = s })
	var p probes
	for c := range b.cands {
		left := time.Until(end)
		if left <= 0 || ctx.Err() != nil {
			break
		}
		if err := probeHub(ctx, opts, tok, c, min(lim.try, left)); err != nil {
			p.add(err)
			continue
		}
		b.stop() // st is final once the browse's goroutine is done
		span.SetAttributes(attribute.String("result", "found"), attribute.Int("kept", st.Kept))
		r.log.InfoContext(ctx, "enroll.discovered", "peer", c.addr, "kept", st.Kept, "dropped", st.Dropped,
			"elapsed_ms", st.Elapsed.Milliseconds())
		return c, nil
	}
	b.stop()
	if ctx.Err() != nil {
		// The enroll's time cap or the owner's stop ended the search: no
		// advice about firewalls, which would point the wrong way.
		span.SetAttributes(attribute.String("result", "stopped"))
		if errors.Is(context.Cause(ctx), errEnrollCap) {
			return candidate{}, fmt.Errorf("%w: the enroll ran out of time while looking for the hub on the "+
				"local network; nothing was sent: %w", ErrHubFailed, ctx.Err())
		}
		return candidate{}, fmt.Errorf("conn: enroll stopped while looking for the hub; nothing was sent: %w", ctx.Err())
	}
	err := p.failure(st)
	span.SetAttributes(attribute.String("result", "none"), attribute.Int("kept", st.Kept))
	span.SetStatus(codes.Error, err.kind)
	return candidate{}, err
}

// probeHub runs only the pinned TLS handshake with c, the dial and the
// handshake within budget together: no request follows.
func probeHub(ctx context.Context, opts Options, tok Token, c candidate, budget time.Duration) error {
	ctx, span := opts.Telemetry.Tracer(scope).Start(ctx, "agent.discover.candidate")
	defer span.End()
	span.SetAttributes(attribute.String("peer", c.addr), attribute.String("source", c.source))
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var d net.Dialer
	raw, err := d.DialContext(pctx, "tcp", c.dial)
	if err == nil {
		err = tls.Client(raw, pinnedTLS(tok.CAFingerprint, host)).HandshakeContext(pctx)
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
