package conn

import (
	"context"
	"io"
	"log"
	"net/netip"
	"time"

	"github.com/hashicorp/mdns"
)

// mdnsService is the DNS-SD service type fleetd advertises (02j).
const mdnsService = "_fleetd._tcp"

// mdnsBrowser browses with github.com/hashicorp/mdns (ADR 0023): one
// question from a random port, answers read until the timeout.
type mdnsBrowser struct{}

// NewMDNS is the agent's [Discover]: an mDNS browse for _fleetd._tcp on
// the default multicast interface.
func NewMDNS() Discover { return mdnsBrowser{} }

// Browse runs one query. The library sends each finished answer once
// without blocking, so they are buffered and read at once; it returns only
// when the timeout passes, even after ctx ends. A machine with no usable
// multicast socket gets the library's bind error.
func (mdnsBrowser) Browse(ctx context.Context, timeout time.Duration, found func(Service)) error {
	entries := make(chan *mdns.ServiceEntry, 64)
	done := make(chan error, 1)
	go func() {
		done <- mdns.QueryContext(ctx, &mdns.QueryParam{
			Service: mdnsService, Domain: "local", Timeout: timeout, Entries: entries,
			// Its log lines are unstructured and echo names from the
			// network; the result says what matters.
			Logger: log.New(io.Discard, "", 0),
		})
	}()
	for {
		select {
		case e := <-entries:
			found(serviceOf(e))
		case err := <-done:
			for {
				select {
				case e := <-entries:
					found(serviceOf(e))
				default:
					return err
				}
			}
		}
	}
}

// serviceOf copies an entry at once: the library keeps the pointer and
// may fill it further from later packets.
func serviceOf(e *mdns.ServiceEntry) Service {
	s := Service{Name: e.Name, Host: e.Host, Port: e.Port}
	if a, ok := netip.AddrFromSlice(e.AddrV4); ok {
		s.AddrV4 = a.Unmap()
	}
	if e.AddrV6IPAddr != nil {
		if a, ok := netip.AddrFromSlice(e.AddrV6IPAddr.IP); ok {
			s.AddrV6 = a.WithZone(e.AddrV6IPAddr.Zone)
		}
	}
	return s
}
