package conntest_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// Restart keeps the hub's port taken the whole time: a thief that binds the
// address in a loop, from before the first restart to after the last, never
// gets it, and the hub still answers on that address after.
func TestRestartNeverFreesThePort(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	addr := h.Addr
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
			if ln, err := lc.Listen(context.Background(), "tcp", addr); err == nil {
				stolen.Add(1)
				_ = ln.Close()
			}
		}
	})
	for range 5 {
		h.Restart(t)
	}
	close(stop)
	wg.Wait()
	if n := stolen.Load(); n != 0 {
		t.Errorf("another listener bound %s %d times during the restarts, want 0", addr, n)
	}
	if h.Addr != addr {
		t.Errorf("the hub moved from %s to %s", addr, h.Addr)
	}
	c, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", h.Addr)
	if err != nil {
		t.Fatalf("dial after the restarts: %v", err)
	}
	_ = c.Close()
}
