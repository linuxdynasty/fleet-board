package restore

// WithStepHook returns o with hook run after each step (copy, rewrite,
// checkpoint, keep, swap), so a test can stop the process there.
func WithStepHook(o Options, hook func(step string)) Options {
	o.stepHook = hook
	return o
}

// WithSyncError returns o with err as the error of the folder sync after
// the swap.
func WithSyncError(o Options, err error) Options {
	o.syncErr = err
	return o
}
