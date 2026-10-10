package discovery

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// Reason is why the hub did not advertise: the closed reason label of the
// refused signals (05b).
type Reason string

// Reasons a plan or a start refuses.
const (
	ReasonNoHostname        Reason = "no_hostname"         // the host name cannot make a DNS name
	ReasonNoAddresses       Reason = "no_addresses"        // no unicast non-loopback address to advertise
	ReasonBadListener       Reason = "bad_listener"        // the agent listener has no usable address or port
	ReasonCertMissingTarget Reason = "cert_missing_target" // the served certificate does not name the SRV target
)

const (
	serviceType = "_fleetd._tcp"
	domain      = "local."
)

// Records is what the hub advertises, built by [Plan] and handed to the
// responder. The agent's browser keeps an answer whose instance name ends
// in "._fleetd._tcp.local." and verifies the hub's certificate against Target.
type Records struct {
	// Instance is the service instance label, "fleetd-<first label of the
	// host>": a dot in it would split the service name.
	Instance string
	// Service and Domain are "_fleetd._tcp" and "local.".
	Service, Domain string
	// Target is the SRV target with its closing dot, "fleetd-<host>.local.":
	// the name the server certificate must hold (ca.ServiceTarget).
	Target string
	Port   int
	// IPs are the A and AAAA records of Target: the addresses of Iface, at
	// most one per family, IPv4 first. The agent's library client keeps only
	// the last A record and the last AAAA record it reads, so more than one
	// per family is a gamble on which one is dialed.
	IPs []net.IP
	// Iface is the interface the advertised addresses belong to, and the one
	// the responder joins the mDNS group on.
	Iface string
	// TXT is "proto=<min>-<max>" and nothing else.
	TXT []string
}

// Iface is one network interface as the advertiser sees it; tests make them.
type Iface struct {
	Name  string
	Flags net.Flags
	// Addrs are its unicast addresses, in the order the system lists them
	// (the primary address first).
	Addrs []netip.Addr
}

// Route is the local address the kernel would use for the default route in
// each family (found without sending a packet); the zero value is unknown.
type Route struct{ V4, V6 netip.Addr }

// virtualPrefixes start the names of bridges, tunnels, VM and container
// interfaces: a LAN agent cannot reach the hub through them.
var virtualPrefixes = []string{
	"bridge", "utun", "awdl", "llw", "docker", "veth", "vmnet", "tun", "tap",
	"vboxnet", "tailscale", "br-", "virbr", "wg", "podman", "cni", "lxcbr", "lxdbr",
	"incusbr", "zt", "flannel", "cali", "cilium", "nordlynx", "ppp",
}

// Plan builds the records for a hub on hostname whose agent listener is
// bound at listener, or says why it advertises nothing.
//
// A listener bound to one address advertises that address, on the interface
// that holds it, however virtual that interface is: the owner chose it. A
// listener bound to the wildcard address advertises the addresses of one
// interface, chosen so that a LAN agent can reach it (see choose). Of its
// addresses only the route's source address (else the first listed) is kept
// for each family. Loopback, unspecified, multicast, broadcast and IPv6
// link-local addresses are never advertised (an AAAA record has no zone). An
// IPv4 link-local address (169.254/16, a direct cable with no DHCP) ranks
// last: it is advertised only when nothing else qualifies.
func Plan(hostname string, listener netip.AddrPort, ifaces []Iface, route Route) (Records, Reason) {
	target := ca.ServiceTarget(hostname)
	if target == "" {
		return Records{}, ReasonNoHostname
	}
	if listener.Port() == 0 || !listener.IsValid() {
		return Records{}, ReasonBadListener
	}
	var ifc *Iface
	var chosen []netip.Addr
	if bound := clean(listener.Addr()); !bound.IsUnspecified() {
		if ifc = holder(ifaces, bound); ifc != nil && advertisable(bound) {
			chosen = []netip.Addr{bound}
		}
	} else if ifc = choose(ifaces, route); ifc != nil {
		chosen = pick(ifc, route, true)
	}
	if len(chosen) == 0 {
		return Records{}, ReasonNoAddresses
	}
	ips := make([]net.IP, len(chosen))
	for i, a := range chosen {
		ips[i] = net.IP(a.AsSlice())
	}
	instance, _, _ := strings.Cut(target, ".")
	return Records{
		Instance: instance,
		Service:  serviceType, Domain: domain,
		Target: target + ".",
		Port:   int(listener.Port()),
		IPs:    ips,
		Iface:  ifc.Name,
		TXT:    []string{"proto=" + strconv.Itoa(int(protoversion.Min)) + "-" + strconv.Itoa(int(protoversion.Max))},
	}, ""
}

func clean(a netip.Addr) netip.Addr { return a.Unmap().WithZone("") }

// usable is an interface that can carry mDNS: up, multicast, not loopback.
func usable(i *Iface) bool {
	return i.Flags&net.FlagUp != 0 && i.Flags&net.FlagMulticast != 0 && i.Flags&net.FlagLoopback == 0
}

// reachable is an interface a LAN agent can be expected to reach the hub on.
func reachable(i *Iface) bool {
	if !usable(i) || i.Flags&net.FlagPointToPoint != 0 {
		return false
	}
	return !hasPrefix(i.Name, virtualPrefixes)
}

// holder is the usable interface that holds a, however virtual: the owner
// bound the listener to that address.
func holder(ifaces []Iface, a netip.Addr) *Iface {
	for i := range ifaces {
		for _, x := range ifaces[i].Addrs {
			if usable(&ifaces[i]) && clean(x) == a {
				return &ifaces[i]
			}
		}
	}
	return nil
}

// tunnelPrefixes start the names of tunnel and VPN interfaces. A tunnel holds
// the default route only when its traffic leaves the machine for somewhere a
// LAN agent is not.
var tunnelPrefixes = []string{"tun", "tap", "wg", "utun", "tailscale", "zt", "nordlynx", "ppp"}

func hasPrefix(name string, prefixes []string) bool {
	name = strings.ToLower(name)
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// choose is the interface a LAN agent is expected to reach the hub on. In
// each of two rounds, the first without IPv4 link-local addresses and the
// second with them (they rank last), the first that applies of:
//
//  1. the interface holding the default route's source address, when it is up,
//     multicast, not point-to-point and not named like a tunnel. Its name may
//     look like a bridge: a Linux host that bridges its NIC holds its address
//     and the route on bridge0, and a management NIC beside it is not the way
//     in;
//  2. the first reachable interface (no bridge, container, VM or tunnel name):
//     the guess when no route is known;
//  3. the route holder even when it is named like a tunnel, if it is up,
//     multicast and not point-to-point (a tap device that is the only
//     addressed route holder is advertised).
//
// A point-to-point interface is never used.
func choose(ifaces []Iface, route Route) *Iface {
	holders := func() []*Iface {
		var out []*Iface
		for _, src := range []netip.Addr{route.V4, route.V6} {
			if src.IsValid() {
				if i := holder(ifaces, clean(src)); i != nil {
					out = append(out, i)
				}
			}
		}
		return out
	}()
	for _, linkLocal := range []bool{false, true} {
		for _, i := range holders {
			if i.Flags&net.FlagPointToPoint == 0 && !hasPrefix(i.Name, tunnelPrefixes) && len(pick(i, route, linkLocal)) > 0 {
				return i
			}
		}
		for n := range ifaces {
			if reachable(&ifaces[n]) && len(pick(&ifaces[n], route, linkLocal)) > 0 {
				return &ifaces[n]
			}
		}
		for _, i := range holders {
			if i.Flags&net.FlagPointToPoint == 0 && len(pick(i, route, linkLocal)) > 0 {
				return i
			}
		}
	}
	return nil
}

// pick is one IPv4 and one IPv6 address of i, IPv4 first: the route's source
// address when i holds it, else the first advertisable one listed. An IPv4
// link-local address is used only when linkLocal is set and i has no other
// IPv4 address.
func pick(i *Iface, route Route, linkLocal bool) []netip.Addr {
	r4, r6 := clean(route.V4), clean(route.V6)
	var v4, v6, v4LL netip.Addr
	var v4Route, v6Route bool
	for _, raw := range i.Addrs {
		a := clean(raw)
		switch {
		case !advertisable(a):
		case a.Is4() && a.IsLinkLocalUnicast():
			if !v4LL.IsValid() {
				v4LL = a
			}
		case a.Is4():
			if !v4Route && (!v4.IsValid() || a == r4) {
				v4, v4Route = a, a == r4
			}
		default:
			if !v6Route && (!v6.IsValid() || a == r6) {
				v6, v6Route = a, a == r6
			}
		}
	}
	if !v4.IsValid() && linkLocal {
		v4 = v4LL
	}
	var out []netip.Addr
	for _, a := range []netip.Addr{v4, v6} {
		if a.IsValid() {
			out = append(out, a)
		}
	}
	return out
}

// advertisable is an address another machine can dial: unicast, not this
// machine, and not IPv6 link-local (an AAAA record cannot carry the zone the
// dial needs). IPv4 link-local passes here; [pick] ranks it last.
func advertisable(a netip.Addr) bool {
	switch {
	case !a.IsValid(), a.IsUnspecified(), a.IsMulticast(), a.IsLoopback(),
		a.Is6() && a.IsLinkLocalUnicast(), a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return false
	}
	return true
}
