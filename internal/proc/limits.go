package proc

import (
	"fmt"
	"slices"
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

const kib = 1024

// callers is the defaults row "One-shot child process", in code. A test
// (TestCallersMatchTheDefaultsRow) fails when the two differ in a name or a
// number. The numbers are starting values chosen from the size of each
// command's job, not measured runs: no caller exists yet. The row that adds a
// caller measures its real call and changes its numbers here and in the row,
// in the same PR.
var callers = map[string]Limits{
	"git":      {Time: 120 * time.Second, Output: 1024 * kib},
	"gh":       {Time: 30 * time.Second, Output: 1024 * kib},
	"loginctl": {Time: 5 * time.Second, Output: 16 * kib},
	"tmux":     {Time: 10 * time.Second, Output: 1024 * kib},
	"version":  {Time: 10 * time.Second, Output: 16 * kib},
}

// For returns the caller with that name, or [ErrUnknownCaller] when the
// defaults row has no entry for it.
func For(name string) (Caller, error) {
	l, ok := callers[name]
	if !ok {
		return Caller{}, fmt.Errorf("%w %q: add it to the defaults row \"One-shot child process\" first", ErrUnknownCaller, name)
	}
	return Caller{name: name, limits: l}, nil
}

// Names lists every caller, sorted.
func Names() []string {
	names := make([]string, 0, len(callers))
	for n := range callers {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
