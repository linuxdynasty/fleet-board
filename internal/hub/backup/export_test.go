package backup

// Faults reach the unexported seams from the external tests.
type Faults struct {
	// AfterCopy runs on the copy before its check.
	AfterCopy func(path string) error
	// Encrypt, when set, is the error of the encrypt step.
	Encrypt error
	// Remove replaces os.Remove for temporary files and pruning.
	Remove func(path string) error
}

func (f Faults) seams() seams {
	return seams{afterCopy: f.AfterCopy, encrypt: f.Encrypt, remove: f.Remove}
}

// WithFaults returns o with the seams set.
func (o Options) WithFaults(f Faults) Options {
	o.seams = f.seams()
	return o
}

// WithFaults returns o with the seams set.
func (o ScheduleOptions) WithFaults(f Faults) ScheduleOptions {
	o.seams = f.seams()
	return o
}
