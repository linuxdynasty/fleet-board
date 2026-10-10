package wakenet

import (
	"net/netip"
	"reflect"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// homeReading is a laptop on wired Ethernet with an IPv6 ULA, as on linux-box.
func homeReading() Reading {
	return Reading{
		Interfaces: map[string][]netip.Prefix{
			"lo":     {pfx("127.0.0.1/8"), pfx("::1/128")},
			"enp3s0": {pfx("192.168.1.50/24"), pfx("fd00:1:2:3::50/64"), pfx("fe80::1/64")},
		},
		DefaultV4: Route{Iface: "enp3s0", Gateway: addr("192.168.1.1")},
		DefaultV6: Route{Iface: "enp3s0", Gateway: addr("fe80::1")},
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(r *Reading)
		want   []Change
	}{
		{name: "same reading", change: func(*Reading) {}},
		{
			name:   "interface added",
			change: func(r *Reading) { r.Interfaces["tun0"] = []netip.Prefix{pfx("10.8.0.2/24")} },
			want:   []Change{{Kind: InterfaceAdded, Iface: "tun0"}},
		},
		{
			name:   "interface removed (down or gone)",
			change: func(r *Reading) { delete(r.Interfaces, "enp3s0") },
			want:   []Change{{Kind: InterfaceRemoved, Iface: "enp3s0"}},
		},
		{
			name: "address added and removed",
			change: func(r *Reading) {
				r.Interfaces["enp3s0"] = []netip.Prefix{
					pfx("192.168.1.51/24"), pfx("fd00:1:2:3::50/64"), pfx("fe80::1/64"),
				}
			},
			want: []Change{
				{Kind: AddressAdded, Iface: "enp3s0", Addr: pfx("192.168.1.51/24")},
				{Kind: AddressRemoved, Iface: "enp3s0", Addr: pfx("192.168.1.50/24")},
			},
		},
		{
			name:   "default route moved to another gateway",
			change: func(r *Reading) { r.DefaultV4.Gateway = addr("192.168.1.254") },
			want:   []Change{{Kind: DefaultRouteChanged, Iface: "enp3s0"}},
		},
		{
			name:   "IPv6 default route gone",
			change: func(r *Reading) { r.DefaultV6 = Route{} },
			want:   []Change{{Kind: DefaultRouteChanged, IPv6: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			old, cur := homeReading(), homeReading()
			tc.change(&cur)
			got := Diff(old, cur)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Diff = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDiffIgnoresAddressOrder(t *testing.T) {
	t.Parallel()
	old, cur := homeReading(), homeReading()
	cur.Interfaces["enp3s0"] = []netip.Prefix{pfx("fe80::1/64"), pfx("fd00:1:2:3::50/64"), pfx("192.168.1.50/24")}
	if got := Diff(old, cur); len(got) != 0 {
		t.Errorf("Diff = %+v, want none", got)
	}
}

func TestReadingHolds(t *testing.T) {
	t.Parallel()
	r := homeReading()
	tests := []struct {
		addr netip.Addr
		want bool
	}{
		{addr("192.168.1.50"), true},
		{addr("::ffff:192.168.1.50"), true},
		{addr("fe80::1%enp3s0"), true},
		{addr("192.168.1.99"), false},
		{netip.Addr{}, false},
	}
	for _, tc := range tests {
		if got := r.Holds(tc.addr); got != tc.want {
			t.Errorf("Holds(%v) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
