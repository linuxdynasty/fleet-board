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
//     4 to 11 ms, 10 bytes of output. Capped at 5 s and 1 KiB. Not measured
//     on Linux yet; row 7 measures its other one-shot tmux calls and raises
//     the entry if they need it.
var callers = map[string]Limits{
	"tmux": {Time: 5 * time.Second, Output: 1 << 10},
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
