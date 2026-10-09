package wakenet

import (
	"context"
	"log/slog"
	"net/netip"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Conn is the connection state the policy needs.
type Conn struct {
	Connected bool
	// LocalAddr is the live stream's own local address; zero when unknown.
	LocalAddr netip.Addr
}

// Why is the closed set of reasons behind a decision, for logs and tests.
type Why string

const (
	WhyWake                 Why = "wake"
	WhyNotConnected         Why = "not_connected"
	WhyLocalAddressLost     Why = "local_address_lost"
	WhyDefaultRouteChanged  Why = "default_route_changed"
	WhyConnectionUnaffected Why = "connection_unaffected"
	WhyNoChange             Why = "no_change"
)

// Decision is what the connection loop does with one event. Reconnect resets
// the backoff to its first step and starts an attempt after the short random
// delay (03 A2; the 5 s minimum between connects still applies), closing the
// live stream first when CloseStream is set. Reason goes into Hello.
type Decision struct {
	Reconnect   bool
	CloseStream bool
	Reason      agentv1.ConnectReason
	Why         Why
}

// Decide is the ADR 0029 policy. A wake is one reconnect whether connected or
// not. A network change while not connected is one attempt. While connected,
// only two changes reconnect: the default route changed, or the connection's
// own local address was on an interface that is up and now is on none.
func Decide(ev Event, c Conn) Decision {
	switch ev.Reason {
	case agentv1.ConnectReason_CONNECT_REASON_WAKE:
		return Decision{Reconnect: true, CloseStream: c.Connected, Reason: ev.Reason, Why: WhyWake}
	case agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE:
	default:
		return Decision{Why: WhyNoChange}
	}
	if len(ev.Net.Changes) == 0 {
		return Decision{Why: WhyNoChange}
	}
	act := Decision{Reconnect: true, CloseStream: c.Connected, Reason: ev.Reason}
	if !c.Connected {
		act.Why = WhyNotConnected
		return act
	}
	for _, ch := range ev.Net.Changes {
		if ch.Kind == DefaultRouteChanged {
			act.Why = WhyDefaultRouteChanged
			return act
		}
	}
	if ev.Net.Previous.Holds(c.LocalAddr) && !ev.Net.Current.Holds(c.LocalAddr) {
		act.Why = WhyLocalAddressLost
		return act
	}
	return Decision{Why: WhyConnectionUnaffected}
}

// LogDecision writes netchange.acted or netchange.ignored for a network event.
// A wake already logged wake.detected, so it writes nothing.
func LogDecision(ctx context.Context, log *slog.Logger, ev Event, d Decision) {
	if ev.Reason != agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE {
		return
	}
	if d.Reconnect {
		log.InfoContext(ctx, "netchange.acted", "why", string(d.Why), "close_stream", d.CloseStream,
			"changes", len(ev.Net.Changes))
		return
	}
	log.InfoContext(ctx, "netchange.ignored", "why", string(d.Why), "changes", len(ev.Net.Changes))
}
