package protoversion_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

func TestTheV1WireIsVersionOne(t *testing.T) {
	t.Parallel()
	if protoversion.Min != 1 || protoversion.Max != 1 {
		t.Errorf("range [%d, %d], want [1, 1] (02l)", protoversion.Min, protoversion.Max)
	}
}

func TestNegotiate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		min, max uint32
		want     uint32
		ok       bool
	}{
		{"exactly 1", 1, 1, 1, true},
		{"a newer peer that still speaks 1", 1, 3, 1, true},
		{"unset (0/0)", 0, 0, 0, false},
		{"min unset", 0, 1, 0, false},
		{"max unset", 1, 0, 0, false},
		{"min above max", 2, 1, 0, false},
		{"only newer versions", 2, 5, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := protoversion.Negotiate(tc.min, tc.max)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Negotiate(%d, %d) = %d, %v; want %d, %v", tc.min, tc.max, got, ok, tc.want, tc.ok)
			}
		})
	}
}
