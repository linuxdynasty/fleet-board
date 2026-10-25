package proc

import (
	"fmt"
	"time"
)

// Limits are the two caps of a caller. Both are always set: there is no
// default for a caller that has none.
type Limits struct {
	// Time is how long the child may run, from the start of Run.
	Time time.Duration
	// Output is the most bytes the child may write to stdout and stderr
	// together.
	Output int64
}

// Caller is who runs a child: one of the callers named in the defaults row
// "One-shot child process" (docs/specs/defaults.md). It carries that row's
// caps, so a Spec cannot name a cap of its own. Get one with [For]; the zero
// Caller is refused by [Runner.Run].
type Caller struct {
	name   string
	limits Limits
}

// Name is the caller's name, the `caller` label of every signal.
func (c Caller) Name() string { return c.name }

// Limits are the caller's caps.
func (c Caller) Limits() Limits { return c.limits }

// callers is the defaults row "One-shot child process", in code. The row
// says each cap is set from a measured run of that command; a caller is added
// by the row that first runs it, with its measured entry here and its
// `name` seconds / KiB in the row, in the same PR (TestCallersMatchTheDefaultsRow
// fails when the two differ). A name with no entry gets no default.
//
//   - tmux: `tmux -V`, 50 runs on macOS 27 (arm64), tmux 3.7c, a loaded host:
//     4 to 11 ms, 10 bytes of output. Row 7 (7c2) added `tmux list-sessions
//     -F '#{session_id} #{@fleet_internal}'`: at 200 sessions (the load
//     target) 1,101 bytes, 3.4 to 7.6 ms over 50 runs on tmux 3.2a and 3.7c
//     (macOS). Capped at 5 s and 16 KiB (about 2,900 sessions). Not measured
//     on Linux yet.
//   - version: the harness `--version` commands with empty stdin, 5 runs
//     each on macOS 27 (arm64), a loaded host: worst 0.32 s (cursor-agent)
//     and 22 bytes (claude). Capped at 5 s and 16 KiB, like `tmux`: headroom
//     for a slow machine and for a wrapper that prints an install prompt
//     instead of a version. Row 8 (#141) added it. The `gemini` binary was
//     not installed here, and Linux is not measured yet.
//   - git: `git status --porcelain`, `rev-parse`, `rev-list --count` and
//     `remote get-url`, 3 runs on macOS 26 (arm64) on this repo: 10 to
//     160 ms (cold) and tens of bytes. Row 9 (H1, H3) runs them per
//     checkout once a minute. Capped at 90 s and 64 KiB (about a thousand
//     changed paths); a dirtier tree keeps its last reading.
//   - gh: `gh auth status` and `gh repo view --json viewerPermission`,
//     one run each on macOS 26 (arm64): 0.46 s and 0.38 s, under a
//     kilobyte. Row 9 (H4) re-checks hourly (the forge_cli_status event).
//     Capped at 30 s and 8 KiB for a slow network.
//   - service: the `upgrade` restart, `launchctl kickstart -k
//     gui/<uid>/com.fleet-board.<program>` on macOS or `systemctl --user
//     restart <program>.service` on Linux. Measured on macOS 27 (arm64):
//     a kickstart against a missing service answers in under 10 ms with
//     81 bytes, 5 runs. Capped at 5 s and 4 KiB, like `loginctl`: a real
//     restart stays in the same range, and a wedged manager hits the time
//     cap instead of hanging the upgrade. Row 16 added it. Linux is not
//     measured yet; its caps mirror macOS until they are.
//   - hooks: the goal-hook verify runs (`python3 fleet-goal-hook.py
//     <harness> --input-file <payload>`), 5 runs on macOS 27 (arm64), a
//     loaded host: about 20 ms and under a kilobyte of output. Row 20
//     added it. Capped at 5 s and 16 KiB, like `tmux`: headroom for a
//     slow machine and a cold python3. Linux is not measured yet.
var callers = map[string]Limits{
	"tmux":     {Time: 5 * time.Second, Output: 16 << 10},
	"loginctl": {Time: 5 * time.Second, Output: 4 << 10},
	"version":  {Time: 5 * time.Second, Output: 16 << 10},
	"git":      {Time: 90 * time.Second, Output: 64 << 10},
	"gh":       {Time: 30 * time.Second, Output: 8 << 10},
	"service":  {Time: 5 * time.Second, Output: 4 << 10},
	"hooks":    {Time: 5 * time.Second, Output: 16 << 10},
}

// For returns the caller with that name, or [ErrUnknownCaller] when the
// defaults row has no entry for it.
func For(name string) (Caller, error) { return lookup(callers, name) }

// lookup is For over a given table, so a test can seed one.
func lookup(table map[string]Limits, name string) (Caller, error) {
	l, ok := table[name]
	if !ok {
		return Caller{}, fmt.Errorf("%w %q: measure its call and add it to the defaults row \"One-shot child process\" first", ErrUnknownCaller, name)
	}
	return Caller{name: name, limits: l}, nil
}
