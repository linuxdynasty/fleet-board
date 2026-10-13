package goals

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// harnessBinaries maps a process binary's basename to the harness name
// (typev1 Harness: free string, known values). Only the exact binary
// counts: a wrapper (node, sh) around one is not a harness, and row 8
// owns richer detection.
var harnessBinaries = map[string]string{
	"claude":       "claude",
	"codex":        "codex",
	"muse":         "muse",
	"cursor-agent": "cursor",
	"opencode":     "opencode",
}

// HarnessForCommand names the harness whose binary argv runs, "" when none
// of its words is a known harness binary.
func HarnessForCommand(argv []string) string {
	for _, word := range argv {
		if name, ok := harnessBinaries[strings.ToLower(filepath.Base(word))]; ok {
			return name
		}
	}
	return ""
}

// Procs sees the machine's processes. Implementations walk real processes
// (gopsutil) or a stub for tests.
type Procs interface {
	// HarnessIn names the harness running under pid and the earliest start
	// of such a process. ok is false when no known harness runs there.
	HarnessIn(pid int32) (name string, start time.Time, ok bool)
	// Alive reports whether pid runs now.
	Alive(pid uint32) bool
}

// GopsutilProcs is the real Procs: gopsutil, no cgo, darwin and linux.
type GopsutilProcs struct{}

// HarnessIn walks the processes under pid (breadth-first, capped) and
// matches each one's words against the harness binaries.
func (GopsutilProcs) HarnessIn(pid int32) (string, time.Time, bool) {
	root, err := process.NewProcess(pid)
	if err != nil {
		return "", time.Time{}, false
	}
	var name string
	var start time.Time
	seen := map[int32]bool{pid: true}
	queue := []*process.Process{root}
	for len(queue) > 0 && len(seen) <= maxTreeProcs {
		p := queue[0]
		queue = queue[1:]
		argv, err := p.CmdlineSlice()
		if err != nil || len(argv) == 0 {
			if name, err := p.Name(); err == nil && HarnessForCommand([]string{name}) != "" {
				argv = []string{name}
			}
		}
		if h := HarnessForCommand(argv); h != "" {
			ms, err := p.CreateTime()
			if err == nil {
				t := time.UnixMilli(ms)
				if name == "" || t.Before(start) {
					name, start = h, t
				}
			} else if name == "" {
				name = h
			}
		}
		children, err := p.Children()
		if err != nil {
			continue
		}
		for _, c := range children {
			if !seen[c.Pid] {
				seen[c.Pid] = true
				queue = append(queue, c)
			}
		}
	}
	if name == "" {
		return "", time.Time{}, false
	}
	return name, start, true
}

// Alive reports whether pid runs now.
func (GopsutilProcs) Alive(pid uint32) bool {
	ok, err := process.PidExists(int32(pid)) //nolint:gosec // pids fit int32
	return err == nil && ok
}

// maxTreeProcs caps one pane's process walk: panes hold a shell and a
// harness, never thousands of processes; past the cap the walk stops and
// reports what it found.
const maxTreeProcs = 512
