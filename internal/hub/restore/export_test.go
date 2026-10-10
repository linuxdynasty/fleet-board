package restore

// WithStepHook returns o with hook run after each step (copy, rewrite,
// checkpoint, keep, swap), so a test can stop the process there.
func WithStepHook(o Options, hook func(step string)) Options {
	o.stepHook = hook
	return o
}
