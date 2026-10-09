//go:build darwin || linux

package wakenet

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
)

// SystemReader reads this machine's network: interfaces and addresses through
// the standard library (netlink on Linux, the routing sysctl on macOS), and
// the default routes from the kernel routing table. No exec.
func SystemReader() Reader { return sysReader{} }

type sysReader struct{}

func (sysReader) Read() (Reading, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return Reading{}, fmt.Errorf("list interfaces: %w", err)
	}
	names := make(map[int]string, len(ifs))
	r := Reading{Interfaces: make(map[string][]netip.Prefix)}
	for _, ifc := range ifs {
		names[ifc.Index] = ifc.Name
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagRunning == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return Reading{}, fmt.Errorf("addresses of %s: %w", ifc.Name, err)
		}
		ps := make([]netip.Prefix, 0, len(addrs))
		for _, a := range addrs {
			if p, ok := ipNetPrefix(a); ok && !slices.Contains(ps, p) {
				ps = append(ps, p)
			}
		}
		r.Interfaces[ifc.Name] = ps
	}
	r.DefaultV4, r.DefaultV6, err = defaultRoutes(names)
	if err != nil {
		return Reading{}, err
	}
	return r, nil
}

// ipNetPrefix turns an interface address into a prefix, IPv4 in its 4-byte form.
func ipNetPrefix(a net.Addr) (netip.Prefix, bool) {
	ipn, ok := a.(*net.IPNet)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, bits := ipn.Mask.Size()
	ip, ok := netip.AddrFromSlice(ipn.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
		if bits == 128 {
			ones -= 96
		}
	}
	return netip.PrefixFrom(ip, ones), true
}
