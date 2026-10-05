package main

import (
	"reflect"
	"testing"
)

func TestParseBrowse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []string
		want  []BrowseEvent
	}{
		{name: "no output", lines: nil, want: nil},
		{
			name: "header lines are ignored, add and remove parsed, spaces kept in the name",
			lines: []string{
				"Browsing for _fleetd._tcp.local.",
				"DATE: ---Mon 05 Oct 2026---",
				"10:55:00.123  ...STARTING...",
				"Timestamp     A/R    Flags  if Domain               Service Type         Instance Name",
				"10:55:00.456  Add        3  14 local.               _fleetd._tcp.        fleet-spike-mini",
				"10:55:00.457  Add        2   7 local.               _fleetd._tcp.        my test box",
				"10:55:09.001  Rmv        0  14 local.               _fleetd._tcp.        fleet-spike-mini",
			},
			want: []BrowseEvent{
				{Action: "Add", Flags: "3", IfIndex: "14", Domain: "local.", Type: "_fleetd._tcp.", Instance: "fleet-spike-mini"},
				{Action: "Add", Flags: "2", IfIndex: "7", Domain: "local.", Type: "_fleetd._tcp.", Instance: "my test box"},
				{Action: "Rmv", Flags: "0", IfIndex: "14", Domain: "local.", Type: "_fleetd._tcp.", Instance: "fleet-spike-mini"},
			},
		},
		{
			name:  "pty output: CR line ends and the EOF echo before the first line",
			lines: []string{"\x04\b\bBrowsing for _fleetd._tcp.local.\r", "11:00:00.000  Add        2  14 local.               _fleetd._tcp.        test\r"},
			want:  []BrowseEvent{{Action: "Add", Flags: "2", IfIndex: "14", Domain: "local.", Type: "_fleetd._tcp.", Instance: "test"}},
		},
		{
			name:  "short line is not an event",
			lines: []string{"10:55:00.456  Add        3  14 local."},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseBrowse(tc.lines); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseBrowse = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseLookup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{name: "nothing reached", lines: []string{"Lookup x._fleetd._tcp.local", "10:55:00.000  ...STARTING..."}, want: nil},
		{
			name: "reached lines are kept from the instance name on",
			lines: []string{
				"Lookup fleet-spike._fleetd._tcp.local",
				"10:55:00.100  fleet-spike._fleetd._tcp.local. can be reached at fleet-spike.local.:47999 (interface 14)",
				" txtvers=1 role=spike",
			},
			want: []string{"fleet-spike._fleetd._tcp.local. can be reached at fleet-spike.local.:47999 (interface 14)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseLookup(tc.lines); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseLookup = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMatchInstance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, instance, entry string
		want                  bool
	}{
		{"empty filter matches anything", "", "x._fleetd._tcp.local.", true},
		{"exact instance", "fleet-spike", "fleet-spike._fleetd._tcp.local.", true},
		{"prefix of a longer name is not a match", "fleet", "fleet-spike._fleetd._tcp.local.", false},
		{"escaped space in the wire name", "my box", `my\ box._fleetd._tcp.local.`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := matchInstance(tc.instance, tc.entry); got != tc.want {
				t.Fatalf("matchInstance(%q, %q) = %v, want %v", tc.instance, tc.entry, got, tc.want)
			}
		})
	}
}
