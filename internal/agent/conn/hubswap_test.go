package conn_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A hub swap never frees its port: a thief that binds the address in a loop
// from before the swap to after it never gets it, and the new server takes the
// connections that come afterwards.
func TestAHubSwapNeverFreesThePort(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	var stolen atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		var lc net.ListenConfig
		for {
			select {
			case <-stop:
				return
			default:
			}
			if ln, err := lc.Listen(context.Background(), "tcp", h.addr); err == nil {
				stolen.Add(1)
				_ = ln.Close()
			}
		}
	})
	for range 20 {
		h.swap(t)
	}
	close(stop)
	wg.Wait()
	if n := stolen.Load(); n != 0 {
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
