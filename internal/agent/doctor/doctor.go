package doctor

import (
	"context"
	"time"
)

// Status is the outcome of one check: five words, no more.
type Status string

// The five statuses. Only [Fail] makes `fleet-agent doctor` exit 1.
const (
	// OK means the check ran and found nothing wrong.
	OK Status = "ok"
	// Warn means something is off but the agent still works.
	Warn Status = "warn"
	// Fail means the agent cannot work, or is not safe, until the owner acts.
	Fail Status = "fail"
	// Unknown means the check ran but the fact is not there: a status-file
	// value never set, a file that is missing, a tool that would not run.
	Unknown Status = "unknown"
	// NotChecked means this build has no code for the check yet; the reason
	// names the row that adds it.
	NotChecked Status = "not_checked"
)

func (s Status) valid() bool {
	switch s {
	case OK, Warn, Fail, Unknown, NotChecked:
		return true
	}
	return false
}

// Outcome is what a check returns: a status, why, and what to do about it.
type Outcome struct {
	Status Status
	// Reason is one plain sentence. Every check has one, whatever the status.
	Reason string
	// Fix says what the owner does next; empty for ok and not_checked.
	Fix string
}

// Check is one named check. Run reads from env and returns an Outcome; it
// must not write, create or remove anything. A check added by a later row
// (the harness trust screens) is one more Check passed to [Run].
type Check struct {
	// ID is the stable machine name ("state_dir").
	ID string
	// Title is the short human name ("State folder").
	Title string
	Run   func(ctx context.Context, env *Env) Outcome
}

// Result is a finished check as it is printed.
type Result struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Reason string `json:"reason"`
	Fix    string `json:"fix,omitempty"`
}

// Summary counts the results by status.
type Summary struct {
	OK         int `json:"ok"`
	Warn       int `json:"warn"`
	Fail       int `json:"fail"`
	Unknown    int `json:"unknown"`
	NotChecked int `json:"not_checked"`
}

// Report is the whole run.
type Report struct {
	StateDir    string    `json:"state_dir"`
	ConfigDir   string    `json:"config_dir"`
	CheckedTime time.Time `json:"checked_time"`
	Summary     Summary   `json:"summary"`
	Checks      []Result  `json:"checks"`
}

// Failed reports whether any check failed: the exit code is 1 then.
func (r Report) Failed() bool { return r.Summary.Fail > 0 }

// Run runs the checks in order and returns the report. A check that panics is
// reported unknown with a fixed sentence (the panic text is never printed) and
// the rest still run. Every string in the report is cleaned and bounded here,
// whatever the check returned, because it is printed on a terminal and some of
// it comes from files an attacker might have written.
func Run(ctx context.Context, opts Options, checks ...Check) Report {
	env := newEnv(opts)
	rep := Report{
		StateDir: clean(opts.StateDir, maxPath), ConfigDir: clean(opts.ConfigDir, maxPath),
		CheckedTime: env.Now().UTC().Truncate(time.Second),
		Checks:      make([]Result, 0, len(checks)),
	}
	for _, c := range checks {
		out := runOne(ctx, env, c)
		if !out.Status.valid() {
			out = Outcome{Status: Unknown, Reason: "the check returned no valid status", Fix: "report this as a bug in fleet-agent doctor"}
		}
		rep.Checks = append(rep.Checks, Result{
			ID: clean(c.ID, maxTitle), Title: clean(c.Title, maxTitle), Status: out.Status,
			Reason: clean(out.Reason, maxText), Fix: clean(out.Fix, maxText),
		})
		rep.Summary.add(out.Status)
	}
	return rep
}

func runOne(ctx context.Context, env *Env, c Check) (out Outcome) {
	if err := ctx.Err(); err != nil {
		return Outcome{Status: Unknown, Reason: "doctor was stopped before this check ran", Fix: "run fleet-agent doctor again"}
	}
	defer func() {
		if recover() != nil {
			out = Outcome{Status: Unknown, Reason: "this check stopped on an internal error", Fix: "report this as a bug in fleet-agent doctor"}
		}
	}()
	return c.Run(ctx, env)
}

func (s *Summary) add(st Status) {
	switch st {
	case OK:
		s.OK++
	case Warn:
		s.Warn++
	case Fail:
		s.Fail++
	case Unknown:
		s.Unknown++
	case NotChecked:
		s.NotChecked++
	}
}

func ok(reason string) Outcome { return Outcome{Status: OK, Reason: reason} }

func warn(reason, fix string) Outcome { return Outcome{Status: Warn, Reason: reason, Fix: fix} }

func fail(reason, fix string) Outcome { return Outcome{Status: Fail, Reason: reason, Fix: fix} }

func unknown(reason, fix string) Outcome { return Outcome{Status: Unknown, Reason: reason, Fix: fix} }
