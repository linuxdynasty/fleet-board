//go:build !fleet_nomdns

package main

import (
	"reflect"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// A release build finds the hub with the real mDNS browser; only the tag
// the tests build the binary with swaps it.
func TestAReleaseBuildBrowsesWithMDNS(t *testing.T) {
	t.Parallel()
	if got, want := reflect.TypeOf(defaultDiscover()), reflect.TypeOf(conn.NewMDNS()); got != want {
		t.Errorf("defaultDiscover() is %v, want %v", got, want)
	}
}
