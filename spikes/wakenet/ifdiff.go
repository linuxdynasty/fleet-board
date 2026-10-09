package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
)

// IfState is one interface as method (b) sees it.
type IfState struct {
	Name  string
	Up    bool
	Addrs []string // sorted CIDR strings
}

// Snapshot is every interface, keyed by name.
type Snapshot map[string]IfState

// Change is one difference between two snapshots.
type Change struct {
	Iface  string
	Kind   string // iface_added, iface_removed, up, down, addr_added, addr_removed
	Detail string
}

// Capture reads every interface and its addresses (net.Interfaces is pure Go:
// netlink on Linux, the sysctl routing table dump on macOS).
func Capture() (Snapshot, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	s := make(Snapshot, len(ifs))
	for _, ifc := range ifs {
		st := IfState{Name: ifc.Name, Up: ifc.Flags&net.FlagUp != 0 && ifc.Flags&net.FlagRunning != 0}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, fmt.Errorf("addresses of %s: %w", ifc.Name, err)
		}
		for _, a := range addrs {
			st.Addrs = append(st.Addrs, a.String())
		}
		sort.Strings(st.Addrs)
		s[ifc.Name] = st
	}
	return s, nil
}

// Diff lists what changed from old to new, ordered by interface name.
func Diff(old, new Snapshot) []Change {
	names := make(map[string]struct{}, len(old)+len(new))
	for n := range old {
		names[n] = struct{}{}
	}
	for n := range new {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	var out []Change
	for _, n := range sorted {
		o, inOld := old[n]
		w, inNew := new[n]
		switch {
		case !inOld:
			out = append(out, Change{Iface: n, Kind: "iface_added"})
			continue
		case !inNew:
			out = append(out, Change{Iface: n, Kind: "iface_removed"})
			continue
		}
		if o.Up != w.Up {
			kind := "down"
			if w.Up {
				kind = "up"
			}
			out = append(out, Change{Iface: n, Kind: kind})
		}
		for _, a := range missing(w.Addrs, o.Addrs) {
			out = append(out, Change{Iface: n, Kind: "addr_added", Detail: a})
		}
		for _, a := range missing(o.Addrs, w.Addrs) {
			out = append(out, Change{Iface: n, Kind: "addr_removed", Detail: a})
		}
	}
	return out
}

// missing returns the entries of a that are not in b.
func missing(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, x := range b {
		set[x] = struct{}{}
	}
	var out []string
	for _, x := range a {
		if _, ok := set[x]; !ok {
			out = append(out, x)
		}
	}
	return out
}

// DefaultRoute is the IPv4 default route; the zero value means none.
type DefaultRoute struct {
	Iface   string
	Gateway string
}

// parseProcNetRoute reads Linux /proc/net/route and returns the default route
// with the lowest metric. Addresses there are little-endian hex.
func parseProcNetRoute(r io.Reader) DefaultRoute {
	var best DefaultRoute
	bestMetric := -1
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		metric, err := strconv.Atoi(f[6])
		if err != nil {
			continue
		}
		gw, err := hex.DecodeString(f[2])
		if err != nil || len(gw) != 4 {
			continue
		}
		if bestMetric >= 0 && metric >= bestMetric {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(gw))
		best, bestMetric = DefaultRoute{Iface: f[0], Gateway: ip.String()}, metric
	}
	return best
}

// parseRouteGetDefault reads the output of macOS `route -n get default`.
func parseRouteGetDefault(out string) DefaultRoute {
	var d DefaultRoute
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch k {
		case "gateway":
			d.Gateway = strings.TrimSpace(v)
		case "interface":
			d.Iface = strings.TrimSpace(v)
		}
	}
	if d.Iface == "" {
		return DefaultRoute{}
	}
	return d
}
