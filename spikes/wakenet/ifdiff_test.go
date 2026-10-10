package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	t.Parallel()
	en0 := IfState{Name: "en0", Up: true, Addrs: []string{"192.0.2.40/24", "fe80::1/64"}}
	cases := []struct {
		name     string
		old, new Snapshot
		want     []Change
	}{
		{
			name: "identical snapshots",
			old:  Snapshot{"en0": en0},
			new:  Snapshot{"en0": en0},
		},
		{
			name: "interface appears",
			old:  Snapshot{"en0": en0},
			new:  Snapshot{"en0": en0, "utun9": {Name: "utun9", Up: true}},
			want: []Change{{Iface: "utun9", Kind: "iface_added"}},
		},
		{
			name: "interface disappears",
			old:  Snapshot{"en0": en0, "utun9": {Name: "utun9", Up: true}},
			new:  Snapshot{"en0": en0},
			want: []Change{{Iface: "utun9", Kind: "iface_removed"}},
		},
		{
			name: "link goes down and loses its IPv4 address",
			old:  Snapshot{"en0": en0},
			new:  Snapshot{"en0": {Name: "en0", Up: false, Addrs: []string{"fe80::1/64"}}},
			want: []Change{
				{Iface: "en0", Kind: "down"},
				{Iface: "en0", Kind: "addr_removed", Detail: "192.0.2.40/24"},
			},
		},
		{
			name: "new DHCP address on the same link",
			old:  Snapshot{"en0": en0},
			new:  Snapshot{"en0": {Name: "en0", Up: true, Addrs: []string{"10.0.0.7/24", "fe80::1/64"}}},
			want: []Change{
				{Iface: "en0", Kind: "addr_added", Detail: "10.0.0.7/24"},
				{Iface: "en0", Kind: "addr_removed", Detail: "192.0.2.40/24"},
			},
		},
		{
			name: "changes are ordered by interface name",
			old:  Snapshot{},
			new:  Snapshot{"lo0": {Name: "lo0", Up: true}, "en0": en0},
			want: []Change{{Iface: "en0", Kind: "iface_added"}, {Iface: "lo0", Kind: "iface_added"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Diff(tc.old, tc.new)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Diff = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseProcNetRoute(t *testing.T) {
	t.Parallel()
	header := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"
	cases := []struct {
		name  string
		table string
		want  DefaultRoute
	}{
		{
			name: "single default via 192.168.1.1",
			table: header +
				"enp3s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
				"enp3s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n",
			want: DefaultRoute{Iface: "enp3s0", Gateway: "192.168.1.1"},
		},
		{
			name: "lowest metric default wins",
			table: header +
				"wlan0\t00000000\t0100000A\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
				"enp3s0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			want: DefaultRoute{Iface: "enp3s0", Gateway: "192.168.1.1"},
		},
		{
			name:  "no default route",
			table: header + "enp3s0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n",
		},
		{
			name:  "malformed lines are skipped",
			table: header + "garbage\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseProcNetRoute(strings.NewReader(tc.table))
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRouteGetDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  string
		want DefaultRoute
	}{
		{
			name: "macOS route -n get default",
			out: "   route to: default\ndestination: default\n       mask: default\n    gateway: 192.168.1.1\n" +
				"  interface: en0\n      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>\n",
			want: DefaultRoute{Iface: "en0", Gateway: "192.168.1.1"},
		},
		{
			name: "no default route",
			out:  "route: writing to routing socket: not in table\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRouteGetDefault(tc.out); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
