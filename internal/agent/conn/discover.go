package conn

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Discovery limits (defaults.md "mDNS browse timeout", "mDNS answer
// budget", "mDNS phase budget"; ADR 0028).
const (
	// DefaultBrowseTimeout bounds one mDNS browse.
	DefaultBrowseTimeout = 2 * time.Second
	// mdnsTryBudget bounds the dial and the TLS handshake of one mDNS
	// answer together, so an answer that accepts and never replies cannot
	// hold up the rest.
	mdnsTryBudget = 3 * time.Second
	// mdnsPhaseBudget bounds the mDNS part of one attempt (the browse and
	// the answers it gave), after which the configured addresses are tried.
	mdnsPhaseBudget = 8 * time.Second
)

// fleetdSuffix ends the name of every answer discovery keeps (ADR 0028:
// the library returns unrelated services too).
const (
	fleetdSuffix   = "._fleetd._tcp.local."
	maxServiceName = 255
)

// Why an answer was dropped: a closed set, logged as counts.
const (
	dropNotHubName = "not_hub_name"
	dropBadPort    = "bad_port"
	dropBadHost    = "bad_host"
	dropBadAddr    = "bad_addr"
	dropOverCap    = "over_cap"
)

// Service is one mDNS answer. It is untrusted data from the network: only
// a candidate until TLS verifies it against the pinned CA (02g).
type Service struct {
	// Name is the instance name, such as "fleetd-mac._fleetd._tcp.local.".
	Name string
	// Host is the SRV target, such as "mac.local.": the name the hub's
	// certificate must hold.
	Host   string
	Port   int
	AddrV4 netip.Addr
	// AddrV6 carries its zone when it is link-local.
	AddrV6 netip.Addr
}

// Discover finds hubs on the local network (03 A2, ADR 0023 and 0028).
type Discover interface {
	// Browse looks for hubs until timeout passes or ctx ends and calls
	// found, from one goroutine, once for each distinct answer.
	Browse(ctx context.Context, timeout time.Duration, found func(Service)) error
}

// BrowseStatus is the last mDNS browse, kept in [Status].
type BrowseStatus struct {
	Time    time.Time
	Elapsed time.Duration
	// Kept answers became candidates; Dropped ones did not, counted in
	// DroppedBy by reason (not_hub_name, bad_port, bad_host, bad_addr,
	// over_cap). An answer repeated within the browse is neither.
	Kept, Dropped int
	DroppedBy     map[string]int
	// ErrKind is set when the browse failed (no usable multicast
	// interface) and kept nothing: the 05b err_kind.
	ErrKind string
	// lateErrKind is a query that failed after earlier ones kept answers:
	// the browse ran, and this is only logged.
	lateErrKind string
}

// mdnsLimits are the discovery limits; zero fields take the defaults.
// Tests shorten them and let loopback answers through (export_test.go).
type mdnsLimits struct {
	try, phase time.Duration
	loopbackOK bool
}

func (m mdnsLimits) withDefaults() mdnsLimits {
	if m.try <= 0 {
		m.try = mdnsTryBudget
	}
	if m.phase <= 0 {
		m.phase = mdnsPhaseBudget
	}
	return m
}

// candidateOf turns an mDNS answer into a candidate: the SRV target and
// port name the hub (its certificate must hold that name), and the
// answer's address is dialed. Anything else is dropped, with the reason.
func candidateOf(s Service, loopbackOK bool) (candidate, string) {
	name := strings.ToLower(s.Name)
	if len(name) > maxServiceName || len(name) <= len(fleetdSuffix) || !strings.HasSuffix(name, fleetdSuffix) {
		return candidate{}, dropNotHubName
	}
	if s.Port < 1 || s.Port > 65535 {
		return candidate{}, dropBadPort
	}
	host := strings.ToLower(strings.TrimRight(s.Host, "."))
	addr, err := HubAddress(net.JoinHostPort(host, strconv.Itoa(s.Port)))
	if host == "" || err != nil || selfName(host) {
		return candidate{}, dropBadHost
	}
	ip, ok := dialable(s.AddrV4, loopbackOK)
	if !ok {
		ip, ok = dialable(s.AddrV6, loopbackOK)
	}
	if !ok {
		return candidate{}, dropBadAddr
	}
	return candidate{addr: addr, dial: netip.AddrPortFrom(ip, uint16(s.Port)).String(), source: "mdns"}, ""
}

// selfName is a target that names this machine itself: an answer that says
// so would have the agent cache localhost as its hub.
func selfName(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().IsLoopback() || ip.IsUnspecified()
	}
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// dialable is an answer's address the agent may dial: unicast, never this
// machine (an untrusted answer must not point the agent at its own
// services; tests on loopback allow it), and a link-local IPv6 address
// only with its zone. An IPv4 address inside IPv6 is judged as IPv4.
func dialable(a netip.Addr, loopbackOK bool) (netip.Addr, bool) {
	a = a.Unmap()
	switch {
	case !a.IsValid(), a.IsUnspecified(), a.IsMulticast(), a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return a, false
	case a.IsLoopback():
		return a, loopbackOK
	case a.Is6() && a.IsLinkLocalUnicast() && a.Zone() == "":
		return a, false
	}
	return a, true
}

// browse is one mDNS browse running on its own goroutine: its kept
// answers arrive on cands as they come, and cands closes when it ends.
type browse struct {
	cands  chan candidate
	done   chan struct{}
	cancel context.CancelFunc
}

// startBrowse starts one browse. report runs on the browse's goroutine
// when it ends, before cands closes, with the time to the first kept
// answer (negative when none).
func startBrowse(ctx context.Context, d Discover, timeout time.Duration, lim mdnsLimits,
	report func(st BrowseStatus, first time.Duration),
) *browse {
	if timeout <= 0 {
		timeout = DefaultBrowseTimeout
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &browse{cands: make(chan candidate, maxHubAddresses), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(b.done)
		start := time.Now()
		st := BrowseStatus{Time: start}
		first := time.Duration(-1)
		seen := map[string]bool{}
		drop := func(why string) {
			st.Dropped++
			if st.DroppedBy == nil {
				st.DroppedBy = map[string]int{}
			}
			st.DroppedBy[why]++
		}
		err := d.Browse(bctx, timeout, func(s Service) {
			c, why := candidateOf(s, lim.loopbackOK)
			switch {
			case why != "":
				drop(why)
			case seen[c.key()]:
			case st.Kept == maxHubAddresses:
				drop(dropOverCap)
			default:
				seen[c.key()] = true
				st.Kept++
				if first < 0 {
					first = time.Since(start)
				}
				// Never blocks: at most maxHubAddresses are kept.
				b.cands <- c
			}
		})
		st.Elapsed = time.Since(start)
		switch {
		case err != nil && st.Kept == 0:
			st.ErrKind = errKind(err)
		case err != nil:
			st.lateErrKind = errKind(err)
		}
		report(st, first)
		close(b.cands)
	}()
	return b
}

// stop ends the browse and waits for its goroutine.
func (b *browse) stop() {
	b.cancel()
	for range b.cands {
	}
	<-b.done
}
