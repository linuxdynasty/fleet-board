package conn

import (
	"os"
	"runtime"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// BasicFacts are the machine facts enroll sends before the full B1 facts
// exist: hostname, OS, architecture, CPU count, UTC offset and agent
// version. The hub compares the hostname with the token's expected one.
func BasicFacts(version string) *agentv1.MachineFacts {
	host, _ := os.Hostname()
	_, offset := time.Now().Zone()
	return &agentv1.MachineFacts{
		Hostname:         clean(host, maxNameBytes),
		Os:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		CpuCount:         uint32(min(runtime.NumCPU(), 1<<16)), //nolint:gosec // G115: capped above
		UtcOffsetSeconds: int32(offset),                        //nolint:gosec // G115: a zone offset is under a day in seconds
		AgentVersion:     version,
	}
}
