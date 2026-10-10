package machine_test

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// TestRealSampleOnThisMachine takes one sample from the real machine through
// the real gopsutil and platform readers, and logs it (go test -v) so the PR
// can paste it. Every reading must work on macOS and Linux.
func TestRealSampleOnThisMachine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := one(t, machine.SamplerOptions{StateDir: dir, Roots: func() []string { return []string{"/"} }})
	r := s.Latest().Resources
	if r.Load1 == nil || r.Load5 == nil || r.GetLoad1() < 0 {
		t.Errorf("load not read: %v", r)
	}
	if r.GetMemTotalBytes() == 0 || r.GetMemAvailableBytes() == 0 || r.GetMemAvailableBytes() > r.GetMemTotalBytes() {
		t.Errorf("memory not read: %v", r)
	}
	if l := r.GetMemPressureLevel(); l != 1 && l != 2 && l != 4 {
		t.Errorf("memory pressure level = %d", l)
	}
	if len(r.GetDisk()) != 2 {
		t.Fatalf("disk entries = %d, want the state folder and /", len(r.GetDisk()))
	}
	for _, d := range r.GetDisk() {
		if d.GetTotalBytes() == 0 || d.GetFreeBytes() > d.GetTotalBytes() {
			t.Errorf("disk %q free %d total %d", d.GetPath(), d.GetFreeBytes(), d.GetTotalBytes())
		}
	}
	t.Logf("%s", protojson.Format(r))
}
