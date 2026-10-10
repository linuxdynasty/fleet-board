package conntest_test

import (
	"context"
	"net"
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
	thief := conntest.StartThief(t, addr)
	for range 5 {
		h.Restart(t)
	}
	if n := thief.Stolen(); n != 0 {
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
