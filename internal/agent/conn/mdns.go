package conn

import (
	"context"
	"io"
	"log"
	"net"
	"net/netip"
	"runtime"
	"time"

	"github.com/hashicorp/mdns"
)

// mdnsService is the DNS-SD service type fleetd advertises (02j).
const mdnsService = "_fleetd._tcp"

// mdnsWindow is one query of a browse. The library keeps writing an answer
// it already handed over while later packets arrive, until its query
// returns, so an answer is read only after that; short queries keep the
// first answer within one window of arriving.
const mdnsWindow = 500 * time.Millisecond

// querier is mdns.QueryContext; tests replace it.
type querier func(ctx context.Context, p *mdns.QueryParam) error

// mdnsBrowser browses with github.com/hashicorp/mdns (ADR 0023): a run of
// short queries, each from a random port, until the browse time is up.
type mdnsBrowser struct {
	query  querier
	window time.Duration
	// noIPv6 asks over IPv4 only. On macOS the library's IPv6 query has no
	// route without an interface, and one failed send aborts the whole
	// query (spike 0.7 finding 3).
	noIPv6 bool
	// iface is the multicast interface; nil is the system's default.
	iface *net.Interface
}

// NewMDNS is the agent's [Discover]: an mDNS browse for _fleetd._tcp on
// the default multicast interface.
func NewMDNS() Discover { return newMDNS() }

func newMDNS() mdnsBrowser {
	return mdnsBrowser{query: mdns.QueryContext, window: mdnsWindow, noIPv6: runtime.GOOS == "darwin"}
}

// Browse runs queries of one window each until timeout passes or ctx
// ends, and hands each distinct answer over once its query has returned.
// A machine with no usable multicast socket gets the library's error.
func (m mdnsBrowser) Browse(ctx context.Context, timeout time.Duration, found func(Service)) error {
	end := time.Now().Add(timeout)
	seen := map[Service]bool{}
	for ctx.Err() == nil {
		left := time.Until(end)
		if left <= 0 {
			return nil
		}
		// The library hands over each finished answer once without
		// blocking: a full buffer drops it, and the next window asks again.
		entries := make(chan *mdns.ServiceEntry, 64)
		err := m.query(ctx, &mdns.QueryParam{
			Service: mdnsService, Domain: "local", Timeout: min(m.window, left), Entries: entries,
			DisableIPv6: m.noIPv6, Interface: m.iface,
			// Its log lines are unstructured and echo names from the
			// network; the result says what matters.
			Logger: log.New(io.Discard, "", 0),
		})
		// The query has returned: nothing writes these entries any more.
		for drained := false; !drained; {
			select {
			case e := <-entries:
				if s := serviceOf(e); !seen[s] {
					seen[s] = true
					found(s)
				}
			default:
				drained = true
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// serviceOf copies an entry's fields.
func serviceOf(e *mdns.ServiceEntry) Service {
	s := Service{Name: e.Name, Host: e.Host, Port: e.Port}
	if a, ok := netip.AddrFromSlice(e.AddrV4); ok {
		s.AddrV4 = a.Unmap()
	}
	if e.AddrV6IPAddr != nil {
		if a, ok := netip.AddrFromSlice(e.AddrV6IPAddr.IP); ok {
			if a = a.Unmap(); a.Is6() {
				a = a.WithZone(e.AddrV6IPAddr.Zone)
			}
			s.AddrV6 = a
		}
	}
	return s
}
