package machine

import (
	"context"
	"fmt"
	"os"

	"github.com/shirou/gopsutil/v4/process"
)

// ownRSS is the agent's own resident memory (AgentHealth.rss_bytes, ADR
// 0031). The Process is a literal with this pid, not process.NewProcess: that
// constructor looks the pid up and reads its start time, which can start a
// child on macOS. MemoryInfoWithContext reads proc_pidinfo through purego on
// macOS and /proc/<pid>/statm on Linux, in this process, with no child.
//
// A pid that is not ours to read, or a proc_pidinfo that fails, gives an RSS
// of 0 and no error on macOS (gopsutil ignores that return); the sampler
// treats 0 as a failed reading.
func ownRSS(ctx context.Context) (uint64, error) {
	p := &process.Process{Pid: int32(os.Getpid())} //nolint:gosec // G115: a pid fits in int32
	mi, err := p.MemoryInfoWithContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("own resident memory: %w", err)
	}
	if mi == nil {
		return 0, fmt.Errorf("%w: no memory info", errParse)
	}
	return mi.RSS, nil
}
