//go:build darwin || linux

package wakenet_test

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
)

// SinceBoot reads a clock that has run since boot and moves forward.
func TestSinceBootMovesForward(t *testing.T) {
	t.Parallel()
	a, err := wakenet.SinceBoot()
	if err != nil || a <= 0 {
		t.Fatalf("SinceBoot = %v, %v", a, err)
	}
	time.Sleep(20 * time.Millisecond)
	b, err := wakenet.SinceBoot()
	if err != nil || b-a < 10*time.Millisecond {
		t.Fatalf("SinceBoot moved %v in 20ms (%v)", b-a, err)
	}
}
