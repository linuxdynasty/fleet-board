package proc

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
