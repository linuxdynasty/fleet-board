package discovery

import (
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
type libraryResponder struct{ iface *net.Interface }

// NewResponder is the real responder on iface, or on the system's default
// multicast interface when iface is nil.
func NewResponder(iface *net.Interface) Responder { return libraryResponder{iface: iface} }

// CheckBind joins the family's group and leaves it again. mdns.NewServer
// drops a failed family without a word, so this is where it is seen.
func (r libraryResponder) CheckBind(family string) error {
	group, ok := mdnsGroups[family]
	if !ok {
		return fmt.Errorf("discovery: unknown family %q", family)
	}
	c, err := net.ListenMulticastUDP(family, r.iface, group)
	if err != nil {
		return err
	}
	return c.Close()
}

// Serve starts the library's server for rec. Its own log lines are
// unstructured and echo names from the network, so they are dropped; the
// Advertiser's signals say what matters.
func (r libraryResponder) Serve(rec Records) (func(), error) {
	svc, err := mdns.NewMDNSService(rec.Instance, rec.Service, rec.Domain, rec.Target, rec.Port, rec.IPs, rec.TXT)
	if err != nil {
		return nil, err
	}
	srv, err := mdns.NewServer(&mdns.Config{Zone: svc, Iface: r.iface, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		return nil, err
	}
	return func() { _ = srv.Shutdown() }, nil
}

// InterfaceAddrs lists the unicast addresses of this machine's interfaces
// that are up, can multicast and are not loopback. [Plan] filters further.
func InterfaceAddrs() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		const want = net.FlagUp | net.FlagMulticast
		if ifc.Flags&want != want || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					out = append(out, ip.Unmap())
				}
			}
		}
	}
	return out, nil
}
