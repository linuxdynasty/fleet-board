// Package machine reads the machine the agent runs on: the B1 facts
// ([Collector]) and the B2 resource sample ([Sampler]) of
// docs/specs/01-agent-capabilities.md section B.
//
// [Collector.Fill] adds what conn.BasicFacts does not know to a
// MachineFacts: os_version, ram_bytes, timezone, agent_binary_hash,
// tmux_version (`tmux -V` through internal/proc under the measured caller
// "tmux"), container and username. A fact that cannot be read stays empty.
//
// [Sampler.Run] takes a ResourceSample at start and every 30 s: load
// averages, total and available memory, the memory pressure level, and free
// disk under the state folder and each allowed repo root. A reading that
// fails is left unset in the message (the optional fields of
// ResourceSample), never 0; it is counted and logged once per run of
// failures. A sample older than three intervals, counting the time the
// machine slept, is withheld as unknown ([Sampler.Fresh]). The latest sample
// is an atomic pointer, so [Hooks.Heartbeat]
// reads it on the connection loop's sending goroutine without a lock, and
// [Sampler.Subscribe] is the seam row 7's resource_sample event uses.
//
// The readings come from github.com/shirou/gopsutil/v4 (disk, load and mem
// only, three approved calls: see [DefaultReaders]) and from the platform:
// kern.memorystatus_vm_pressure_level on macOS, /proc/pressure/memory with a
// MemAvailable fallback on Linux. Nothing here starts a process except
// through internal/proc, and nothing needs cgo.
package machine
