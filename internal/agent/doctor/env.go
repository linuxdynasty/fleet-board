package doctor

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// StaleAfter is how old status.json may be before the agent that writes it
// is called stopped: the writer rewrites at least once a minute (defaults.md,
// "Agent status file rewrite"), so two minutes leaves room for a slow write.
const StaleAfter = 2 * time.Minute

// Options are the inputs of [Run]. Every reading of the machine goes through
// one of them, so a test can supply another.
type Options struct {
	// StateDir is the agent state folder (--state-dir).
	StateDir string
	// ConfigDir is the config folder that holds policy.yaml (--config-dir).
	ConfigDir string
	// ProbeDir is the usage probe's folder (01 C3).
	ProbeDir string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Getuid is the user the folders must belong to; nil is os.Getuid.
	Getuid func() int
	// TmuxVersion runs `tmux -V` and returns its first line: empty with a nil
	// error when tmux is not installed. The agent's measured proc caller is
	// the only way to start it (machine.TmuxVersion).
	TmuxVersion func(ctx context.Context) (string, error)
}

// Env is what a check reads: the options and the files read once for the whole
// run. A check never writes; Env has no way to.
type Env struct {
	opts Options

	statusRead bool
	status     statusfile.Status
	statusErr  error

	endedRead bool
	ended     conn.EndedRecord
	endedErr  error

	policyRead bool
	policy     policy.Inspection

	identityRead bool
	identity     identityFile
	identityErr  *fileProblem

	renewedRead bool
	renewed     renewedCert
}

func newEnv(o Options) *Env {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Getuid == nil {
		o.Getuid = os.Getuid
	}
	return &Env{opts: o}
}

// StateDir is the agent state folder.
func (e *Env) StateDir() string { return e.opts.StateDir }

// ConfigDir is the config folder.
func (e *Env) ConfigDir() string { return e.opts.ConfigDir }

// Now is the time the run started from.
func (e *Env) Now() time.Time { return e.opts.Now() }

// Status reads status.json once. Its age is [statusfile.Status.Age]; the
// error is [fs.ErrNotExist], [statusfile.ErrDamaged] or
// [statusfile.ErrVersion] for a file doctor can say something about.
func (e *Env) Status() (statusfile.Status, error) {
	if !e.statusRead {
		e.statusRead = true
		e.status, e.statusErr = statusfile.Read(e.opts.StateDir)
	}
	return e.status, e.statusErr
}

// Ended reads ended.json once, through [conn.ReadEnded]: the same read `run`
// makes, and it changes nothing. The error wraps [fs.ErrNotExist] when there
// is no file, and [conn.ErrEndedDamaged] or another refusal when `run` would
// stop on the file.
func (e *Env) Ended() (conn.EndedRecord, error) {
	if !e.endedRead {
		e.endedRead = true
		e.ended, e.endedErr = conn.ReadEnded(e.opts.StateDir)
	}
	return e.ended, e.endedErr
}

// Policy reads and judges policy.yaml once, with no side effect.
func (e *Env) Policy() policy.Inspection {
	if !e.policyRead {
		e.policyRead = true
		e.policy = policy.Inspect(e.opts.ConfigDir)
	}
	return e.policy
}

// freshStatus is the status file when it is whole and recent enough to say
// something about the agent as it is now. old is true for a whole file older
// than [StaleAfter]; err is set when there is no usable file.
func (e *Env) freshStatus() (s statusfile.Status, old bool, err error) {
	s, err = e.Status()
	if err != nil {
		return statusfile.Status{}, false, err
	}
	return s, s.Age(e.Now()) > StaleAfter, nil
}

// noStatus is the outcome of a check that needs status.json when there is
// none to use.
func noStatus(err error) Outcome {
	switch {
	case errors.Is(err, statusfile.ErrDamaged):
		return unknown("status.json is damaged, so this fact is not known", "remove status.json; the running agent writes it again within a minute")
	case errors.Is(err, statusfile.ErrVersion):
		return unknown("status.json is from another version of fleet-agent, so this fact is not known", "run the same version of fleet-agent that writes it")
	default:
		return unknown("no usable status.json, so this fact is not known (the agent has not run here, or never wrote it)", "start the agent with fleet-agent run; it writes status.json within a minute")
	}
}
