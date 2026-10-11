package events

// SetAfterSubmit makes Emit call f with a durable event's seq after its ring
// job is queued and the lock released, before it waits for the commit.
func SetAfterSubmit(e *Emitter, f func(seq uint64)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.afterSubmit = f
}
