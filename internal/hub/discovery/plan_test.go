package discovery_test

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

func ips(t *testing.T, r discovery.Records) []string {
	t.Helper()
	var out []string
	for _, ip := range r.IPs {
		out = append(out, ip.String())
	}
	return out
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestPlan(t *testing.T) {
	t.Parallel()
	machine := addrs("192.168.1.5", "127.0.0.1", "fe80::1", "2001:db8::5", "fd00::7", "0.0.0.0", "224.0.0.251", "255.255.255.255", "::1")
	cases := []struct {
		name     string
		hostname string
		bound    string
		ifaces   []netip.Addr
		wantIPs  []string
		wantHost string // the SRV target
		reason   discovery.Reason
	}{
		{
			"all interfaces: unicast, non-loopback only", "Hub.LOCAL", "[::]:7443", machine,
			[]string{"192.168.1.5", "2001:db8::5", "fd00::7"},
			"fleetd-hub.local.", "",
		},
		{
			"IPv4 wildcard listener", "hub", "0.0.0.0:7443", machine,
			[]string{"192.168.1.5", "2001:db8::5", "fd00::7"},
			"fleetd-hub.local.", "",
		},
		{
			"listener on one address advertises only it", "hub", "10.0.0.9:7443", machine,
			[]string{"10.0.0.9"},
			"fleetd-hub.local.", "",
		},
		{
			"listener on one IPv6 address", "hub", "[2001:db8::5]:7443", machine,
			[]string{"2001:db8::5"},
			"fleetd-hub.local.", "",
		},
		{"loopback listener advertises nothing", "hub", "127.0.0.1:7443", machine, nil, "", discovery.ReasonNoAddresses},
		{"IPv6 loopback listener advertises nothing", "hub", "[::1]:7443", machine, nil, "", discovery.ReasonNoAddresses},
		{"loopback-only host", "hub", "[::]:7443", addrs("127.0.0.1", "::1", "fe80::1"), nil, "", discovery.ReasonNoAddresses},
		{"no interface addresses", "hub", "[::]:7443", nil, nil, "", discovery.ReasonNoAddresses},
		{
			"IPv4 inside IPv6 is IPv4", "hub", "[::]:7443", addrs("::ffff:192.168.1.5", "192.168.1.5"),
			[]string{"192.168.1.5"},
			"fleetd-hub.local.", "",
		},
		{
			"a zone is dropped and link-local IPv6 is never advertised", "hub", "[::]:7443",
			addrs("fe80::1%en0", "2001:db8::5%en0"),
			[]string{"2001:db8::5"},
			"fleetd-hub.local.", "",
		},
		{"link-local listener is refused", "hub", "[fe80::1%en0]:7443", machine, nil, "", discovery.ReasonNoAddresses},
		{
			"duplicates once, IPv4 first", "hub", "[::]:7443", addrs("2001:db8::5", "10.0.0.2", "10.0.0.2", "10.0.0.1"),
			[]string{"10.0.0.1", "10.0.0.2", "2001:db8::5"},
			"fleetd-hub.local.", "",
		},
		{"no host name", "", "[::]:7443", machine, nil, "", discovery.ReasonNoHostname},
		{"a host name that is not a DNS name", "my host", "[::]:7443", machine, nil, "", discovery.ReasonNoHostname},
		{"port zero", "hub", "[::]:0", machine, nil, "", discovery.ReasonBadListener},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, reason := discovery.Plan(tc.hostname, netip.MustParseAddrPort(tc.bound), tc.ifaces)
			if reason != tc.reason {
				t.Fatalf("reason %q, want %q", reason, tc.reason)
			}
			if reason != "" {
				return
			}
			if got := ips(t, r); !reflect.DeepEqual(got, tc.wantIPs) {
				t.Errorf("addresses %q, want %q", got, tc.wantIPs)
			}
			if r.Target != tc.wantHost {
				t.Errorf("SRV target %q, want %q", r.Target, tc.wantHost)
			}
			if want := strings.TrimSuffix(tc.wantHost, ".local."); r.Instance != want {
				t.Errorf("instance %q, want %q", r.Instance, want)
			}
		})
	}
}

// The records are the contract the agent's browser reads (internal/agent/conn):
// a _fleetd._tcp service in local., the listener's port, and a TXT record with
// the protocol range only: no fleet id, version or instance id.
func TestPlanRecords(t *testing.T) {
	t.Parallel()
	r, reason := discovery.Plan("fleet-test-host.local", netip.MustParseAddrPort("[::]:7443"), addrs("192.168.1.5"))
	if reason != "" {
		t.Fatalf("reason %q", reason)
	}
	want := discovery.Records{
		Instance: "fleetd-fleet-test-host", Service: "_fleetd._tcp", Domain: "local.",
		Target: "fleetd-fleet-test-host.local.", Port: 7443, IPs: r.IPs, TXT: []string{"proto=1-1"},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("records\n got %+v\nwant %+v", r, want)
	}
	if len(r.IPs) != 1 || r.IPs[0].String() != "192.168.1.5" {
		t.Errorf("addresses %v", r.IPs)
	}
}
