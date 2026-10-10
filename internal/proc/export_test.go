package proc

// CallerForTest builds a caller with the given caps, for tests only: the real
// callers come from the table, which a test must not grow.
func CallerForTest(name string, l Limits) Caller { return Caller{name: name, limits: l} }
