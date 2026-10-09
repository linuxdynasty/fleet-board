package wakenet

import (
	"net/netip"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

const (
	reasonWake = agentv1.ConnectReason_CONNECT_REASON_WAKE
	reasonNet  = agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE
)

// netEvent builds a network event from the home reading and one edit.
func netEvent(change func(r *Reading)) Event {
	old, cur := homeReading(), homeReading()
	change(&cur)
	return Event{Reason: reasonNet, Net: NetInfo{
		Source: SourcePush, Changes: Diff(old, cur), Previous: old, Current: cur,
	}}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	offline := Conn{}
	online := Conn{Connected: true, LocalAddr: addr("192.168.1.50")}
	addTun := func(r *Reading) { r.Interfaces["tun0"] = []netip.Prefix{pfx("10.8.0.2/24")} }
	dropWired := func(r *Reading) { delete(r.Interfaces, "enp3s0") }
	newGateway := func(r *Reading) { r.DefaultV4.Gateway = addr("192.168.1.254") }
	dropLocal := func(r *Reading) {
		r.Interfaces["enp3s0"] = []netip.Prefix{pfx("fdc7:1:2:3::50/64"), pfx("fe80::1/64")}
	}
	dropOther := func(r *Reading) {
		r.Interfaces["enp3s0"] = []netip.Prefix{pfx("192.168.1.50/24"), pfx("fe80::1/64")}
	}
	// The default routes stay as they were, so only the local-address rule is in play.
	moveLocal := func(r *Reading) {
		delete(r.Interfaces, "enp3s0")
		r.Interfaces["wlan0"] = []netip.Prefix{pfx("192.168.1.50/24")}
	}
	reconnect := func(why Why, closeStream bool, reason agentv1.ConnectReason) Decision {
		return Decision{Reconnect: true, CloseStream: closeStream, Reason: reason, Why: why}
	}
	ignore := func(why Why) Decision { return Decision{Why: why} }

	tests := []struct {
		name string
		ev   Event
		conn Conn
		want Decision
	}{
		{"wake while connected closes the stream and reconnects", Event{Reason: reasonWake}, online, reconnect(WhyWake, true, reasonWake)},
		{"wake while not connected starts an attempt", Event{Reason: reasonWake}, offline, reconnect(WhyWake, false, reasonWake)},

		{"not connected: interface added", netEvent(addTun), offline, reconnect(WhyNotConnected, false, reasonNet)},
		{"not connected: interface removed", netEvent(dropWired), offline, reconnect(WhyNotConnected, false, reasonNet)},
		{"not connected: address removed", netEvent(dropOther), offline, reconnect(WhyNotConnected, false, reasonNet)},
		{"not connected: default route changed", netEvent(newGateway), offline, reconnect(WhyNotConnected, false, reasonNet)},
		{"not connected: no change", netEvent(func(*Reading) {}), offline, ignore(WhyNoChange)},

		{"connected: own local address gone", netEvent(dropLocal), online, reconnect(WhyLocalAddressLost, true, reasonNet)},
		{"connected: interface carrying the local address gone", netEvent(dropWired), online, reconnect(WhyLocalAddressLost, true, reasonNet)},
		{"connected: default route changed", netEvent(newGateway), online, reconnect(WhyDefaultRouteChanged, true, reasonNet)},
		{
			"connected: local address seen through IPv4-mapped form", netEvent(dropLocal),
			Conn{Connected: true, LocalAddr: addr("::ffff:192.168.1.50")},
			reconnect(WhyLocalAddressLost, true, reasonNet),
		},
		{"connected: VPN interface added", netEvent(addTun), online, ignore(WhyConnectionUnaffected)},
		{"connected: another address removed", netEvent(dropOther), online, ignore(WhyConnectionUnaffected)},
		{"connected: local address moved to another interface", netEvent(moveLocal), online, ignore(WhyConnectionUnaffected)},
		{
			"connected: unknown local address and an interface removed", netEvent(dropWired),
			Conn{Connected: true},
			ignore(WhyConnectionUnaffected),
		},
		{"connected: no change", netEvent(func(*Reading) {}), online, ignore(WhyNoChange)},
		{"unknown reason is ignored", Event{}, online, ignore(WhyNoChange)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Decide(tc.ev, tc.conn); got != tc.want {
				t.Errorf("Decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLogDecision(t *testing.T) {
	t.Parallel()
	online := Conn{Connected: true, LocalAddr: addr("192.168.1.50")}
	tests := []struct {
		name    string
		ev      Event
		wantMsg string
		wantWhy string
	}{
		{"acted", netEvent(func(r *Reading) { r.DefaultV4 = Route{} }), "netchange.acted", "default_route_changed"},
		{"ignored", netEvent(func(r *Reading) { r.Interfaces["tun0"] = nil }), "netchange.ignored", "connection_unaffected"},
		{"wake is not a network line", Event{Reason: reasonWake}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logs := newLogCapture()
			LogDecision(t.Context(), logs.logger(), tc.ev, Decide(tc.ev, online))
			total := logs.count("netchange.acted") + logs.count("netchange.ignored")
			if tc.wantMsg == "" {
				if total != 0 {
					t.Fatalf("logged %d network decision lines for a wake, want 0", total)
				}
				return
			}
			if total != 1 || logs.count(tc.wantMsg) != 1 {
				t.Fatalf("want exactly one %s line, got acted=%d ignored=%d",
					tc.wantMsg, logs.count("netchange.acted"), logs.count("netchange.ignored"))
			}
			if v, _ := logs.attr(tc.wantMsg, "why"); v.String() != tc.wantWhy {
				t.Errorf("why = %q, want %q", v.String(), tc.wantWhy)
			}
		})
	}
}
