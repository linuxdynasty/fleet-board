package wakenet

import (
	"net/netip"
	"slices"
)

// Reading is the network as the agent sees it: every interface that is up and
// running with its addresses, and the default route for each IP family.
type Reading struct {
	Interfaces map[string][]netip.Prefix
	DefaultV4  Route
	DefaultV6  Route
}

// Route is one default route; the zero value means there is none.
type Route struct {
	Iface   string
	Gateway netip.Addr // zero when the route names only an interface
}

// Reader reads the network; see [SystemReader].
type Reader interface {
	Read() (Reading, error)
}

// ChangeKind is the closed set of differences between two readings.
type ChangeKind string

const (
	InterfaceAdded      ChangeKind = "interface_added"
	InterfaceRemoved    ChangeKind = "interface_removed"
	AddressAdded        ChangeKind = "address_added"
	AddressRemoved      ChangeKind = "address_removed"
	DefaultRouteChanged ChangeKind = "default_route_changed"
)

// Change is one difference. Addr is set for address changes. For a default
// route change, Iface is the new route's interface ("" when there is none now)
// and IPv6 names the family.
type Change struct {
	Kind  ChangeKind
	Iface string
	Addr  netip.Prefix
	IPv6  bool
}

// Diff lists what changed from old to cur: interfaces in name order, then the
// IPv4 and IPv6 default routes. No change gives nil.
func Diff(old, cur Reading) []Change {
	names := make([]string, 0, len(old.Interfaces)+len(cur.Interfaces))
	for n := range old.Interfaces {
		names = append(names, n)
	}
	for n := range cur.Interfaces {
		if _, ok := old.Interfaces[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)

	var out []Change
	for _, n := range names {
		o, inOld := old.Interfaces[n]
		c, inCur := cur.Interfaces[n]
		switch {
		case !inOld:
			out = append(out, Change{Kind: InterfaceAdded, Iface: n})
			continue
		case !inCur:
			out = append(out, Change{Kind: InterfaceRemoved, Iface: n})
			continue
		}
		for _, p := range missing(c, o) {
			out = append(out, Change{Kind: AddressAdded, Iface: n, Addr: p})
		}
		for _, p := range missing(o, c) {
			out = append(out, Change{Kind: AddressRemoved, Iface: n, Addr: p})
		}
	}
	if old.DefaultV4 != cur.DefaultV4 {
		out = append(out, Change{Kind: DefaultRouteChanged, Iface: cur.DefaultV4.Iface})
	}
	if old.DefaultV6 != cur.DefaultV6 {
		out = append(out, Change{Kind: DefaultRouteChanged, Iface: cur.DefaultV6.Iface, IPv6: true})
	}
	return out
}

// missing returns the prefixes of a that are not in b, in a's order.
func missing(a, b []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range a {
		if !slices.Contains(b, p) {
			out = append(out, p)
		}
	}
	return out
}

// clone returns a copy of r that shares no map or slice with it.
func (r Reading) clone() Reading {
	c := r
	c.Interfaces = make(map[string][]netip.Prefix, len(r.Interfaces))
	for n, ps := range r.Interfaces {
		c.Interfaces[n] = slices.Clone(ps)
	}
	return c
}

// Holds reports whether any interface in the reading has address a. An
// IPv4-mapped IPv6 address matches its IPv4 form; zones are ignored.
func (r Reading) Holds(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, ps := range r.Interfaces {
		for _, p := range ps {
			if p.Addr().Unmap().WithZone("") == a {
				return true
			}
		}
	}
	return false
}
