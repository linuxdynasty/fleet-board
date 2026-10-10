package machine_test

import (
	"context"
	"fmt"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Sampler cost, real readers (go test -run '^$' -bench . -benchmem -count=10).
// Budget (declared before measuring): one sample on a 30 s cadence under 50
// ms, so the sampler is under 0.2 % of one core; a Snapshot's facts under 50
// ms, since the hook runs on the connection loop's sending goroutine.

func benchSampler(b *testing.B, roots int) {
	b.Helper()
	// The first root is real; the rest do not exist, so a failed read is part
	// of the cost.
	rs := []string{"/"}
	for i := 1; i < roots; i++ {
		rs = append(rs, fmt.Sprintf("/tmp/fleet-bench-root-%d", i))
	}
	s, err := machine.NewSampler(machine.SamplerOptions{
		StateDir: b.TempDir(), Roots: func() []string { return rs },
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		s.SampleOnceForBench(context.Background())
	}
}

func BenchmarkSampleStateAndOneRoot(b *testing.B) { benchSampler(b, 1) }
func BenchmarkSampleStateAnd30Roots(b *testing.B) { benchSampler(b, 30) }

func BenchmarkNewCollectorBinaryHash(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := machine.NewCollector(context.Background(), machine.CollectorOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFill is a Snapshot's facts with the real `tmux -V` child process.
func BenchmarkFill(b *testing.B) {
	runner, err := proc.NewRunner(nil)
	if err != nil {
		b.Fatal(err)
	}
	c, err := machine.NewCollector(context.Background(), machine.CollectorOptions{Runner: runner})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		c.Fill(context.Background(), &agentv1.MachineFacts{})
	}
}
