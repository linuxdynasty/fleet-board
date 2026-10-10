package machine

import (
	"context"
	"fmt"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Readers are the four readings a sample takes. A nil field is the real
// reading ([DefaultReaders]); tests and row 7 may replace one. Each returns
// an error instead of a made-up value: a failed reading is unset, never 0.
type Readers struct {
	// Load is the 1 and 5 minute load averages.
	Load func(ctx context.Context) (load1, load5 float64, err error)
	// Mem is total and available memory in bytes.
	Mem func(ctx context.Context) (total, available uint64, err error)
	// Pressure is the memory pressure level (1, 2 or 4). It gets the Mem
	// reading, with memOK false when Mem failed.
	Pressure func(ctx context.Context, total, available uint64, memOK bool) (uint32, error)
	// Usage is the free and total bytes of the filesystem holding path.
	Usage func(ctx context.Context, path string) (free, total uint64, err error)
}

// DefaultReaders reads through gopsutil's load, mem and disk packages and the
// platform's memory pressure source.
//
// Only three gopsutil functions may be called: load.AvgWithContext,
// mem.VirtualMemoryWithContext and disk.UsageWithContext. Others start child
// processes on macOS (sw_vers, ps, system_profiler) outside internal/proc,
// which 01 principle 8 forbids; TestOnlyApprovedGopsutilCalls enforces it.
func DefaultReaders() Readers {
	return Readers{
		Load: func(ctx context.Context) (float64, float64, error) {
			a, err := load.AvgWithContext(ctx)
			if err != nil {
				return 0, 0, fmt.Errorf("load average: %w", err)
			}
			return a.Load1, a.Load5, nil
		},
		Mem: func(ctx context.Context) (uint64, uint64, error) {
			v, err := mem.VirtualMemoryWithContext(ctx)
			if err != nil {
				return 0, 0, fmt.Errorf("virtual memory: %w", err)
			}
			if v.Total == 0 {
				return 0, 0, fmt.Errorf("%w: memory total is 0", errParse)
			}
			return v.Total, v.Available, nil
		},
		Pressure: platformPressure,
		Usage: func(ctx context.Context, path string) (uint64, uint64, error) {
			u, err := disk.UsageWithContext(ctx, path)
			if err != nil {
				return 0, 0, fmt.Errorf("disk usage: %w", err)
			}
			return u.Free, u.Total, nil
		},
	}
}

// withDefaults fills each nil reading with the real one.
func (r Readers) withDefaults() Readers {
	d := DefaultReaders()
	if r.Load == nil {
		r.Load = d.Load
	}
	if r.Mem == nil {
		r.Mem = d.Mem
	}
	if r.Pressure == nil {
		r.Pressure = d.Pressure
	}
	if r.Usage == nil {
		r.Usage = d.Usage
	}
	return r
}
