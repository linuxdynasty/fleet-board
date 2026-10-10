package discovery

import (
	"net"
	"net/netip"
	"slices"
	"strconv"

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
	// Instance is the service instance label, "fleetd-<host>".
	Instance string
	// Service and Domain are "_fleetd._tcp" and "local.".
	Service, Domain string
	// Target is the SRV target with its closing dot, "fleetd-<host>.local.":
	// the name the server certificate must hold (ca.ServiceTarget).
	Target string
	Port   int
	// IPs are the A and AAAA records of Target: explicit, unicast, never
	// loopback, never the OS host name's own addresses by lookup.
	IPs []net.IP
	// TXT is "proto=<min>-<max>" and nothing else.
	TXT []string
}

// Plan builds the records for a hub on hostname whose agent listener is
// bound at listener. A listener bound to the wildcard address advertises
// ifaceAddrs; one bound to a single address advertises only that. Loopback,
// unspecified, multicast and IPv6 link-local addresses (an AAAA record has no
// zone) are never advertised. A non-empty Reason means nothing is advertised.
func Plan(hostname string, listener netip.AddrPort, ifaceAddrs []netip.Addr) (Records, Reason) {
	target := ca.ServiceTarget(hostname)
	if target == "" {
		return Records{}, ReasonNoHostname
	}
	if listener.Port() == 0 || !listener.IsValid() {
		return Records{}, ReasonBadListener
	}
	candidates := ifaceAddrs
	if bound := listener.Addr().Unmap(); !bound.IsUnspecified() {
		candidates = []netip.Addr{bound}
	}
	var kept []netip.Addr
	for _, a := range candidates {
		if a = a.Unmap().WithZone(""); advertisable(a) && !slices.Contains(kept, a) {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		return Records{}, ReasonNoAddresses
	}
	// IPv4 first, each family in address order, so the records do not move
	// with the order the interfaces were listed in.
	slices.SortFunc(kept, func(a, b netip.Addr) int {
		if a.Is4() != b.Is4() {
			if a.Is4() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	ips := make([]net.IP, len(kept))
	for i, a := range kept {
		ips[i] = net.IP(a.AsSlice())
	}
	return Records{
		Instance: target[:len(target)-len(".local")],
		Service:  serviceType, Domain: domain,
		Target: target + ".",
		Port:   int(listener.Port()),
		IPs:    ips,
		TXT:    []string{"proto=" + strconv.Itoa(int(protoversion.Min)) + "-" + strconv.Itoa(int(protoversion.Max))},
	}, ""
}

// advertisable is an address another machine can dial: unicast, not this
// machine, and, for IPv6, not link-local (the record cannot carry the zone
// the agent's dial would need).
func advertisable(a netip.Addr) bool {
	switch {
	case !a.IsValid(), a.IsUnspecified(), a.IsMulticast(), a.IsLoopback(), a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return false
	case a.Is6() && a.IsLinkLocalUnicast():
		return false
	}
	return true
}
