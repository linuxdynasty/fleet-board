package discovery_test

import (
	"net"
	"net/netip"
	"reflect"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

func ips(r discovery.Records) []string {
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

const up = net.FlagUp | net.FlagMulticast

func nic(name string, flags net.Flags, a ...string) discovery.Iface {
	return discovery.Iface{Name: name, Flags: flags, Addrs: addrs(a...)}
}

// dialed is the address the agent dials from an answer. The library's client
// overwrites the IPv4 address on every A record, so the last one survives,
// and the agent dials that one, then the last AAAA (hashicorp/mdns client.go,
// internal/agent/conn mdns.go serviceOf).
func dialed(r discovery.Records) string {
	var v4, v6 string
	for _, ip := range r.IPs {
		if ip.To4() != nil {
			v4 = ip.String()
		} else {
			v6 = ip.String()
		}
	}
	if v4 != "" {
		return v4
	}
	return v6
}

func TestPlan(t *testing.T) {
	t.Parallel()
	lan := nic("en0", up, "10.0.0.5", "fe80::1", "2001:db8::5", "fd00::7")
	machine := []discovery.Iface{
		nic("lo0", up|net.FlagLoopback, "127.0.0.1", "::1"),
		nic("docker0", up, "172.17.0.1"),
		lan,
		nic("bridge100", up, "192.168.64.1"),
		nic("utun3", up|net.FlagPointToPoint, "100.64.0.2"),
		nic("awdl0", up, "fe80::2"),
	}
	cases := []struct {
		name     string
		hostname string
		bound    string
		ifaces   []discovery.Iface
		route    discovery.Route
		wantIPs  []string
		wantNIC  string
		wantDial string
		reason   discovery.Reason
	}{
		{
			name: "the default-route interface, one address per family", hostname: "Hub.LOCAL", bound: "[::]:7443", ifaces: machine,
			route: discovery.Route{V4: netip.MustParseAddr("10.0.0.5")}, wantIPs: []string{"10.0.0.5", "2001:db8::5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "IPv4 wildcard listener is the same", hostname: "hub", bound: "0.0.0.0:7443", ifaces: machine,
			route: discovery.Route{V4: netip.MustParseAddr("10.0.0.5")}, wantIPs: []string{"10.0.0.5", "2001:db8::5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "a Mac: the route says en1, so the VM bridges and en0 are not advertised", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("en0", up, "192.168.1.5"), nic("en1", up, "10.1.1.5"), nic("bridge100", up, "192.168.64.1"), nic("bridge101", up, "192.168.65.1")},
			route:  discovery.Route{V4: netip.MustParseAddr("10.1.1.5")}, wantIPs: []string{"10.1.1.5"}, wantNIC: "en1", wantDial: "10.1.1.5",
		},
		{
			name: "no route known: the first up multicast non-virtual interface", hostname: "hub", bound: "[::]:7443", ifaces: machine,
			wantIPs: []string{"10.0.0.5", "2001:db8::5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "the default route is a tunnel: fall back to the first real interface", hostname: "hub", bound: "[::]:7443", ifaces: machine,
			route: discovery.Route{V4: netip.MustParseAddr("100.64.0.2")}, wantIPs: []string{"10.0.0.5", "2001:db8::5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "a route address no interface holds is no route", hostname: "hub", bound: "[::]:7443", ifaces: machine,
			route: discovery.Route{V4: netip.MustParseAddr("203.0.113.9")}, wantIPs: []string{"10.0.0.5", "2001:db8::5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "IPv4 link-local ranks last: the next interface has a real address", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en0", up, "169.254.3.4", "fe80::1"), nic("en1", up, "10.1.1.5")},
			wantIPs: []string{"10.1.1.5"}, wantNIC: "en1", wantDial: "10.1.1.5",
		},
		{
			name: "several IPv4 on the interface: only the route's source address", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("en0", up, "10.0.0.5", "10.0.0.6", "10.0.0.7")}, route: discovery.Route{V4: netip.MustParseAddr("10.0.0.6")},
			wantIPs: []string{"10.0.0.6"}, wantNIC: "en0", wantDial: "10.0.0.6",
		},
		{
			name: "several IPv4 and IPv6, no route: the first of each, as listed", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en0", up, "10.0.0.6", "2001:db8::9", "10.0.0.5", "2001:db8::1")},
			wantIPs: []string{"10.0.0.6", "2001:db8::9"}, wantNIC: "en0", wantDial: "10.0.0.6",
		},
		{
			name: "the route's IPv6 source address is the one", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("en0", up, "10.0.0.5", "2001:db8::9", "2001:db8::1")}, route: discovery.Route{V6: netip.MustParseAddr("2001:db8::1")},
			wantIPs: []string{"10.0.0.5", "2001:db8::1"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "IPv6 only", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en0", up, "fe80::1", "fd00::7")},
			wantIPs: []string{"fd00::7"}, wantNIC: "en0", wantDial: "fd00::7",
		},
		{
			name: "down, no-multicast and point-to-point interfaces are skipped", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{
				nic("en5", net.FlagMulticast, "10.5.5.5"), nic("en6", net.FlagUp, "10.6.6.6"),
				nic("en7", up|net.FlagPointToPoint, "10.7.7.7"), nic("en0", up, "10.0.0.5"),
			},
			wantIPs: []string{"10.0.0.5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "virtual interface names are skipped", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{
				nic("bridge0", up, "10.9.0.1"), nic("utun2", up, "10.9.0.2"), nic("awdl0", up, "10.9.0.3"), nic("llw0", up, "10.9.0.4"),
				nic("docker0", up, "10.9.0.5"), nic("veth1", up, "10.9.0.6"), nic("vmnet8", up, "10.9.0.7"), nic("tun0", up, "10.9.0.8"),
				nic("tap0", up, "10.9.0.9"), nic("vboxnet0", up, "10.9.0.10"), nic("tailscale0", up, "100.64.0.3"), nic("br-1a2b", up, "10.9.0.11"),
				nic("virbr0", up, "10.9.0.12"), nic("wg0", up, "10.9.0.13"), nic("podman0", up, "10.9.0.14"),
				nic("cni0", up, "10.9.0.15"), nic("lxcbr0", up, "10.9.0.16"), nic("lxdbr0", up, "10.9.0.17"), nic("incusbr0", up, "10.9.0.18"),
				nic("ztabcdef", up, "10.9.0.19"), nic("flannel.1", up, "10.9.0.20"), nic("cali1234", up, "10.9.0.21"), nic("cilium_host", up, "10.9.0.22"),
				nic("nordlynx", up, "10.9.0.23"), nic("eth0", up, "192.168.1.8"),
			},
			wantIPs: []string{"192.168.1.8"}, wantNIC: "eth0", wantDial: "192.168.1.8",
		},
		{
			name: "a KVM host: the default route is on bridge0, which has the only real address", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("enp3s0", up), nic("bridge0", up, "192.168.1.20"), nic("virbr0", up, "192.168.122.1")},
			route:  discovery.Route{V4: netip.MustParseAddr("192.168.1.20")}, wantIPs: []string{"192.168.1.20"}, wantNIC: "bridge0", wantDial: "192.168.1.20",
		},
		{
			name: "a KVM host with no route known advertises nothing", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("enp3s0", up), nic("bridge0", up, "192.168.1.20"), nic("virbr0", up, "192.168.122.1")}, reason: discovery.ReasonNoAddresses,
		},
		{
			name: "a real interface wins over a filtered route holder", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("bridge0", up, "192.168.1.20"), nic("eth1", up, "10.2.2.2")},
			route:  discovery.Route{V4: netip.MustParseAddr("192.168.1.20")}, wantIPs: []string{"10.2.2.2"}, wantNIC: "eth1", wantDial: "10.2.2.2",
		},
		{
			name: "a tunnel that holds the default route is not used, even with nothing else", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("en0", up), nic("ppp0", up|net.FlagPointToPoint, "100.64.0.2")},
			route:  discovery.Route{V4: netip.MustParseAddr("100.64.0.2")}, reason: discovery.ReasonNoAddresses,
		},
		{
			name: "a route holder that is down is not used", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("bridge0", net.FlagMulticast, "192.168.1.20")},
			route:  discovery.Route{V4: netip.MustParseAddr("192.168.1.20")}, reason: discovery.ReasonNoAddresses,
		},
		{
			name: "a direct cable: only IPv4 link-local, advertised because nothing else exists", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en5", up, "169.254.10.2", "fe80::1")},
			wantIPs: []string{"169.254.10.2"}, wantNIC: "en5", wantDial: "169.254.10.2",
		},
		{
			name: "IPv4 link-local is last on its own interface", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en0", up, "169.254.3.4", "10.0.0.5", "2001:db8::5")},
			wantIPs: []string{"10.0.0.5", "2001:db8::5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "a real address on a later interface wins over link-local on an earlier one", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en5", up, "169.254.10.2"), nic("en0", up, "10.0.0.5")},
			wantIPs: []string{"10.0.0.5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "a listener bound to an IPv4 link-local address", hostname: "hub", bound: "169.254.10.2:7443",
			ifaces:  []discovery.Iface{nic("en5", up, "169.254.10.2")},
			wantIPs: []string{"169.254.10.2"}, wantNIC: "en5", wantDial: "169.254.10.2",
		},
		{
			name: "route sources of both families on different interfaces: IPv4 decides", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en0", up, "10.0.0.5"), nic("en1", up, "10.1.1.5", "2001:db8::9")},
			route:   discovery.Route{V4: netip.MustParseAddr("10.0.0.5"), V6: netip.MustParseAddr("2001:db8::9")},
			wantIPs: []string{"10.0.0.5"}, wantNIC: "en0", wantDial: "10.0.0.5",
		},
		{
			name: "only an IPv6 route source: that interface", hostname: "hub", bound: "[::]:7443",
			ifaces:  []discovery.Iface{nic("en0", up, "10.0.0.5"), nic("en1", up, "10.1.1.5", "2001:db8::9")},
			route:   discovery.Route{V6: netip.MustParseAddr("2001:db8::9")},
			wantIPs: []string{"10.1.1.5", "2001:db8::9"}, wantNIC: "en1", wantDial: "10.1.1.5",
		},
		{
			name: "a listener bound to an address on a down interface", hostname: "hub", bound: "10.5.5.5:7443",
			ifaces: []discovery.Iface{nic("en5", net.FlagMulticast, "10.5.5.5")}, reason: discovery.ReasonNoAddresses,
		},
		{
			name: "a listener bound to an address on an interface without multicast", hostname: "hub", bound: "10.6.6.6:7443",
			ifaces: []discovery.Iface{nic("en6", net.FlagUp, "10.6.6.6")}, reason: discovery.ReasonNoAddresses,
		},
		{
			name: "a listener on one address advertises only it, on its interface", hostname: "hub", bound: "172.17.0.1:7443", ifaces: machine,
			route: discovery.Route{V4: netip.MustParseAddr("10.0.0.5")}, wantIPs: []string{"172.17.0.1"}, wantNIC: "docker0", wantDial: "172.17.0.1",
		},
		{
			name: "a listener on one IPv6 address", hostname: "hub", bound: "[2001:db8::5]:7443", ifaces: machine,
			wantIPs: []string{"2001:db8::5"}, wantNIC: "en0", wantDial: "2001:db8::5",
		},
		{name: "a listener on an address no interface holds", hostname: "hub", bound: "10.9.9.9:7443", ifaces: machine, reason: discovery.ReasonNoAddresses},
		{name: "loopback listener advertises nothing", hostname: "hub", bound: "127.0.0.1:7443", ifaces: machine, reason: discovery.ReasonNoAddresses},
		{name: "IPv6 loopback listener advertises nothing", hostname: "hub", bound: "[::1]:7443", ifaces: machine, reason: discovery.ReasonNoAddresses},
		{name: "link-local listener is refused", hostname: "hub", bound: "[fe80::1%en0]:7443", ifaces: machine, reason: discovery.ReasonNoAddresses},
		{
			name: "loopback-only host", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("lo0", up|net.FlagLoopback, "127.0.0.1", "::1")}, reason: discovery.ReasonNoAddresses,
		},
		{name: "no interfaces", hostname: "hub", bound: "[::]:7443", reason: discovery.ReasonNoAddresses},
		{
			name: "IPv4 inside IPv6 is IPv4", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("en0", up, "::ffff:192.168.1.5")}, wantIPs: []string{"192.168.1.5"}, wantNIC: "en0", wantDial: "192.168.1.5",
		},
		{
			name: "a zone is dropped", hostname: "hub", bound: "[::]:7443",
			ifaces: []discovery.Iface{nic("en0", up, "2001:db8::5%en0")}, wantIPs: []string{"2001:db8::5"}, wantNIC: "en0", wantDial: "2001:db8::5",
		},
		{name: "no host name", hostname: "", bound: "[::]:7443", ifaces: machine, reason: discovery.ReasonNoHostname},
		{name: "a host name that is not a DNS name", hostname: "my host", bound: "[::]:7443", ifaces: machine, reason: discovery.ReasonNoHostname},
		{name: "port zero", hostname: "hub", bound: "[::]:0", ifaces: machine, reason: discovery.ReasonBadListener},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, reason := discovery.Plan(tc.hostname, netip.MustParseAddrPort(tc.bound), tc.ifaces, tc.route)
			if reason != tc.reason {
				t.Fatalf("reason %q, want %q", reason, tc.reason)
			}
			if reason != "" {
				return
			}
			if got := ips(r); !reflect.DeepEqual(got, tc.wantIPs) {
				t.Errorf("addresses %q, want %q", got, tc.wantIPs)
			}
			if r.Iface != tc.wantNIC {
				t.Errorf("interface %q, want %q", r.Iface, tc.wantNIC)
			}
			if got := dialed(r); got != tc.wantDial {
				t.Errorf("the agent would dial %s, want %s", got, tc.wantDial)
			}
			if n4, n6 := count(r); n4 > 1 || n6 > 1 {
				t.Errorf("%d IPv4 and %d IPv6 records; a last-wins client keeps one of each", n4, n6)
			}
		})
	}
}

func count(r discovery.Records) (v4, v6 int) {
	for _, ip := range r.IPs {
		if ip.To4() != nil {
			v4++
		} else {
			v6++
		}
	}
	return v4, v6
}

// The records are the contract the agent's browser reads (internal/agent/conn):
// a _fleetd._tcp service in local., the listener's port, and a TXT record with
// the protocol range only: no fleet id, version or instance id.
func TestPlanRecords(t *testing.T) {
	t.Parallel()
	r, reason := discovery.Plan("fleet-test-host.local", netip.MustParseAddrPort("[::]:7443"),
		[]discovery.Iface{nic("en0", up, "192.168.1.5")}, discovery.Route{})
	if reason != "" {
		t.Fatalf("reason %q", reason)
	}
	want := discovery.Records{
		Instance: "fleetd-fleet-test-host", Service: "_fleetd._tcp", Domain: "local.",
		Target: "fleetd-fleet-test-host.local.", Port: 7443, IPs: r.IPs, TXT: []string{"proto=1-1"}, Iface: "en0",
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("records\n got %+v\nwant %+v", r, want)
	}
}

// A fully qualified host name gives a target with the whole host and an
// instance of its first label only: a dot in an instance label would split
// the service name.
func TestPlanInstanceUsesTheFirstLabelOnly(t *testing.T) {
	t.Parallel()
	r, reason := discovery.Plan("Hub.Example.COM", netip.MustParseAddrPort("[::]:7443"),
		[]discovery.Iface{nic("en0", up, "192.168.1.5")}, discovery.Route{})
	if reason != "" {
		t.Fatalf("reason %q", reason)
	}
	if r.Instance != "fleetd-hub" || r.Target != "fleetd-hub.example.com.local." {
		t.Errorf("instance %q, target %q", r.Instance, r.Target)
	}
}
