package main

import (
	"reflect"
	"testing"
)

func TestParseLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want Line
	}{
		{"begin", "%begin 1728378000 12 1", Line{Kind: "begin", Num: 12, Flags: 1}},
		{"end not from client", "%end 1728378000 3 0", Line{Kind: "end", Num: 3}},
		{"error", "%error 1728378000 7 1", Line{Kind: "error", Num: 7, Flags: 1}},
		{"output", `%output %3 a\015\012`, Line{Kind: "output", Pane: "%3", Value: `a\015\012`}},
		{"output empty", "%output %3", Line{Kind: "output", Pane: "%3"}},
		{
			"extended output",
			"%extended-output %4 1250 : yes\\012",
			Line{Kind: "extended-output", Pane: "%4", Value: "yes\\012"},
		},
		{"pause", "%pause %4", Line{Kind: "pause", Pane: "%4"}},
		{"continue", "%continue %4", Line{Kind: "continue", Pane: "%4"}},
		{
			"session subscription",
			"%subscription-changed sess_act $0 - - - : 1728378001",
			Line{Kind: "subscription-changed", Name: "sess_act", Session: "$0", Window: "-", Pane: "-", Value: "1728378001"},
		},
		{
			"pane subscription",
			"%subscription-changed p $0 @2 1 %5 : x : y",
			Line{Kind: "subscription-changed", Name: "p", Session: "$0", Window: "@2", Pane: "%5", Value: "x : y"},
		},
		{
			"window subscription",
			"%subscription-changed win_act $0 @1 0 - : 1728378002",
			Line{Kind: "subscription-changed", Name: "win_act", Session: "$0", Window: "@1", Pane: "-", Value: "1728378002"},
		},
		{
			"subscription with empty value",
			"%subscription-changed clients $1 - - - : ",
			Line{Kind: "subscription-changed", Name: "clients", Session: "$1", Window: "-", Pane: "-"},
		},
		{"exit with reason", "%exit server exited", Line{Kind: "exit", Value: "server exited"}},
		{"exit bare", "%exit", Line{Kind: "exit"}},
		{"other notification", "%client-detached client-1", Line{Kind: "client-detached", Value: "client-1"}},
		{"reply body", "work: 1 windows", Line{Kind: "data", Value: "work: 1 windows"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseLine(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseLine(%q)\n got %+v\nwant %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBlockState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		lines   []string
		want    []Reply
		inBlock []bool
	}{
		{
			name:    "ok reply with body",
			lines:   []string{"%begin 1 5 1", "a b", "%end 1 5 1"},
			want:    []Reply{{Num: 5, Flags: 1, Body: []string{"a b"}}},
			inBlock: []bool{true, true, true},
		},
		{
			name:    "error reply",
			lines:   []string{"%begin 1 6 1", "unknown flag -f", "%error 1 6 1"},
			want:    []Reply{{Num: 6, Flags: 1, Error: true, Body: []string{"unknown flag -f"}}},
			inBlock: []bool{true, true, true},
		},
		{
			name:    "notification outside block",
			lines:   []string{"%output %1 x", "%begin 1 7 1", "%end 1 7 1"},
			want:    []Reply{{Num: 7, Flags: 1}},
			inBlock: []bool{false, true, true},
		},
		{
			name:    "body line that looks like end of another command",
			lines:   []string{"%begin 1 8 1", "%end 1 2 1", "%end 1 8 1"},
			want:    []Reply{{Num: 8, Flags: 1, Body: []string{"%end 1 2 1"}}},
			inBlock: []bool{true, true, true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var b blockState
			var got []Reply
			var in []bool
			for _, raw := range tc.lines {
				r, ib := b.feed(ParseLine(raw), raw)
				in = append(in, ib)
				if r != nil {
					got = append(got, *r)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replies\n got %+v\nwant %+v", got, tc.want)
			}
			if !reflect.DeepEqual(in, tc.inBlock) {
				t.Fatalf("inBlock got %v want %v", in, tc.inBlock)
			}
		})
	}
}
