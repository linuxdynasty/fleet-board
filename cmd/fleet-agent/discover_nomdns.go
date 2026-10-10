//go:build fleet_nomdns

package main

import (
	"context"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// defaultDiscover in a test build finds nothing and sends nothing, so the
// binary's tests never put a query on the network.
func defaultDiscover() conn.Discover { return none{} }

type none struct{}

func (none) Browse(context.Context, time.Duration, func(conn.Service)) error { return nil }
