package proc

import "os"

// CallerForTest builds a caller with the given caps, for tests only: the real
// callers come from the table, which a test must not grow.
func CallerForTest(name string, l Limits) Caller { return Caller{name: name, limits: l} }

// Table is a copy of the caller table, for the test that compares it with the
// defaults row.
func Table() map[string]Limits {
	out := make(map[string]Limits, len(callers))
	for k, v := range callers {
		out[k] = v
	}
	return out
}

// LookupForTest is For over a seeded table.
func LookupForTest(table map[string]Limits, name string) (Caller, error) {
	return lookup(table, name)
}

// KillGroupOf is the guarded group kill Run's Cancel makes.
func KillGroupOf(p *os.Process) error { return killGroupOf(p) }
