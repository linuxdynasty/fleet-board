package conn_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// A hub swap never frees its port: a thief that binds the address in a loop
// from before the swap to after it never gets it, and the new server takes the
// connections that come afterwards.
func TestAHubSwapNeverFreesThePort(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	thief := conntest.StartThief(t, h.addr)
	for range 20 {
		h.swap(t)
	}
	if n := thief.Stolen(); n != 0 {
		t.Errorf("another listener bound %s %d times during the swaps, want 0", h.addr, n)
	}
	before := h.open.Load()
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", h.addr)
	if err != nil {
		t.Fatalf("dial after the swaps: %v", err)
	}
	defer func() { _ = c.Close() }()
	waitFor(t, 5*time.Second, "the new server to accept", func() bool { return h.open.Load() > before })
}
