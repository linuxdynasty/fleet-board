package discovery

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"

	"github.com/hashicorp/mdns"
)

// mdnsGroups are the multicast groups the responder joins, by family.
var mdnsGroups = map[string]*net.UDPAddr{
	"udp4": {IP: net.ParseIP("224.0.0.251"), Port: 5353},
	"udp6": {IP: net.ParseIP("ff02::fb"), Port: 5353},
}

// libraryResponder is the real [Responder]: github.com/hashicorp/mdns
// (ADR 0023). It listens and answers; it sends nothing until asked.
type libraryResponder struct{}

// NewResponder is the real responder.
func NewResponder() Responder { return libraryResponder{} }

// CheckBind joins the family's group on the interface and leaves it again.
// mdns.NewServer drops a failed family without a word, so this is where it
// is seen.
func (libraryResponder) CheckBind(iface, family string) error {
	group, ok := mdnsGroups[family]
	if !ok {
		return fmt.Errorf("discovery: unknown family %q", family)
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return err
	}
	c, err := net.ListenMulticastUDP(family, ifi, group)
	if err != nil {
		return err
	}
	return c.Close()
}

// Serve starts the library's server for rec on rec.Iface, the interface the
// addresses belong to (ADR 0028 decision 1: it works "with the interface
// set"). Its own log lines are unstructured and echo names from the network,
// so they are dropped; the Advertiser's signals say what matters.
func (libraryResponder) Serve(rec Records) (func(), error) {
	ifi, err := net.InterfaceByName(rec.Iface)
	if err != nil {
		return nil, err
	}
	svc, err := mdns.NewMDNSService(rec.Instance, rec.Service, rec.Domain, rec.Target, rec.Port, rec.IPs, rec.TXT)
	if err != nil {
		return nil, err
	}
	srv, err := mdns.NewServer(&mdns.Config{Zone: svc, Iface: ifi, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		return nil, err
	}
	return func() { _ = srv.Shutdown() }, nil
}

// Interfaces lists this machine's interfaces with their unicast addresses,
// in the order the system gives them.
func Interfaces() ([]Iface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(all))
	for _, ifc := range all {
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, err
		}
		it := Iface{Name: ifc.Name, Flags: ifc.Flags}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					it.Addrs = append(it.Addrs, ip.Unmap())
				}
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// RouteSource finds the local address the kernel would use to reach a
// public address in each family. Connecting a UDP socket picks the route and
// sends nothing; a family with no route stays unknown.
func RouteSource() Route {
	return Route{V4: routeSource("udp4", "192.0.2.1:9"), V6: routeSource("udp6", "[2001:db8::1]:9")}
}

func routeSource(network, remote string) netip.Addr {
	var d net.Dialer
	c, err := d.DialContext(context.Background(), network, remote)
	if err != nil {
		return netip.Addr{}
	}
	defer func() { _ = c.Close() }()
	if u, ok := c.LocalAddr().(*net.UDPAddr); ok {
		if a, ok := netip.AddrFromSlice(u.IP); ok {
			return a.Unmap().WithZone("")
		}
	}
	return netip.Addr{}
}
