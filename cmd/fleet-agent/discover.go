//go:build !fleet_nomdns

package main

import "github.com/linuxdynasty/fleet-board/internal/agent/conn"

// defaultDiscover is how enroll and run find the hub: mDNS.
func defaultDiscover() conn.Discover { return conn.NewMDNS() }
