package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// File is the name of the log inside the agent state folder.
const File = "audit.log"

// Action is one of the machine-side rows of the 02g audit table. The set is
// closed: [Log.Append] refuses any other name.
type Action string

// The actions the agent writes locally (02g, "Audit of decisions made at the
// machine"; 02m has the full catalogue).
const (
	CommandRefused       Action = "command.refused"
	CommandLocalApproval Action = "command.local_approval"
	MachineLocalDrain    Action = "machine.local_drain"
	MachineLocalUndrain  Action = "machine.local_undrain"
	PolicyChanged        Action = "policy.changed"
	PolicySafeMode       Action = "policy.safe_mode"
)

func (a Action) known() bool {
	switch a {
	case CommandRefused, CommandLocalApproval, MachineLocalDrain, MachineLocalUndrain, PolicyChanged, PolicySafeMode:
		return true
	default:
		return false
	}
}

// Defaults of defaults.md, "Agent local audit log".
const (
	// DefaultMaxBytes is the size at which the log rotates (05b: 10 MiB).
	DefaultMaxBytes int64 = 10 << 20
	// Files is how many files are kept: audit.log, audit.log.1, audit.log.2.
	Files = 3
	// warnEvery is how often a failing log says so (once a minute).
	warnEvery = time.Minute
	maxDetail = 4
)

// closed is the one shape every free field must have: letters, digits and
// _ . : - up to 128 bytes. A message, a path or file text cannot match.
var closed = regexp.MustCompile(`^[A-Za-z0-9_.:-]{0,128}$`)

// Principal is who asked for the audited thing: the original requester of a
// refused command. It is never the agent's own OS user.
type Principal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Entry is one audit row. Every field is a closed value (see package doc).
type Entry struct {
	// Time is the agent clock; zero means now.
	Time        time.Time
	Action      Action
	CommandID   string
	Kind        string
	Reason      string
	Status      string
	RequestedBy *Principal
	// Detail holds closed values: old_hash and new_hash, or decision and
	// channel. At most four keys.
	Detail map[string]string
}

// line is the JSON written for an Entry; field order is the file's order.
type line struct {
	Time        string            `json:"time"`
	Action      Action            `json:"action"`
	CommandID   string            `json:"command_id,omitempty"`
	Kind        string            `json:"kind,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Status      string            `json:"status,omitempty"`
	RequestedBy *Principal        `json:"requested_by,omitempty"`
	Detail      map[string]string `json:"detail,omitempty"`
}

// errNotClosed marks an entry that has a free-text field or an unknown action.
var errNotClosed = errors.New("audit: an entry field is not a closed value")

func (e Entry) validate() error {
	if !e.Action.known() {
		return fmt.Errorf("%w: unknown action", errNotClosed)
	}
	fields := []string{e.CommandID, e.Kind, e.Reason, e.Status}
	if p := e.RequestedBy; p != nil {
		fields = append(fields, p.Kind, p.ID)
	}
	if len(e.Detail) > maxDetail {
		return fmt.Errorf("%w: more than %d detail keys", errNotClosed, maxDetail)
	}
	for k, v := range e.Detail {
		if k == "" {
			return fmt.Errorf("%w: empty detail key", errNotClosed)
		}
		fields = append(fields, k, v)
	}
	for _, f := range fields {
		if !closed.MatchString(f) {
			return errNotClosed
		}
	}
	return nil
}

// Options are the inputs of [New].
type Options struct {
	Telemetry *telemetry.Telemetry
	// Logger is the logger for audit.write_failed, with its component and
	// machine_id already set; the default is Telemetry.Logger with
	// component "audit".
	Logger *slog.Logger
	// Now replaces time.Now for entry times and the warning pace; tests only.
	Now func() time.Time
	// MaxBytes is the rotation size; zero is [DefaultMaxBytes].
	MaxBytes int64
}

// Log appends to audit.log. It is safe for concurrent use; one process owns
// the file (the run lock of the state folder).
type Log struct {
	dir      string
	log      *slog.Logger
	now      func() time.Time
	maxBytes int64
	failures metric.Int64Counter

	mu         sync.Mutex // guards everything below, and the file
	f          *os.File
	size       int64
	lastWarn   time.Time
	suppressed int
}

// New returns a log for the state folder. It does not touch the file: the
// first [Log.Append] opens it, so a folder that cannot be written never stops
// the agent from starting.
func New(dir string, opts Options) (*Log, error) {
	if opts.Telemetry == nil {
		return nil, errors.New("audit: Telemetry is required")
	}
	l := &Log{
		dir: dir, log: opts.Logger, now: opts.Now, maxBytes: opts.MaxBytes,
	}
	if l.log == nil {
		l.log = opts.Telemetry.Logger.With("component", "audit")
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.maxBytes <= 0 {
		l.maxBytes = DefaultMaxBytes
	}
	var err error
	l.failures, err = opts.Telemetry.Meter("github.com/linuxdynasty/fleet-board/internal/agent/audit").Int64Counter(
		opts.Telemetry.MetricPrefix()+"_audit_write_failures_total",
		metric.WithDescription("Audit entries that could not be written to audit.log, by err_kind (constraint, io)."))
	if err != nil {
		return nil, fmt.Errorf("audit: register the failure counter: %w", err)
	}
	return l, nil
}

// Append writes one entry as a JSON line and syncs it. It returns nothing:
// a write that fails never blocks the caller or the product. The failure is
// counted in fleet_agent_audit_write_failures_total and logged as
// audit.write_failed at most once a minute; the hub's permanent audit table
// is the security record.
func (l *Log) Append(e Entry) {
	err := l.append(e)
	if err == nil {
		return
	}
	kind := "io"
	if errors.Is(err, errNotClosed) {
		kind = "constraint"
	}
	l.failures.Add(context.Background(), 1, metric.WithAttributes(attribute.String("err_kind", kind)))
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.lastWarn.IsZero() && now.Sub(l.lastWarn) < warnEvery {
		l.suppressed++
		return
	}
	l.log.Warn("audit.write_failed", "err_kind", kind, "suppressed", l.suppressed)
	l.lastWarn, l.suppressed = now, 0
}

func (l *Log) append(e Entry) error {
	if err := e.validate(); err != nil {
		return err
	}
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	b, err := json.Marshal(line{
		Time: e.Time.UTC().Format(time.RFC3339), Action: e.Action, CommandID: e.CommandID, Kind: e.Kind,
		Reason: e.Reason, Status: e.Status, RequestedBy: e.RequestedBy, Detail: e.Detail,
	})
	if err != nil {
		return fmt.Errorf("audit: encode: %w", err)
	}
	b = append(b, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.ensureOpen(int64(len(b))); err != nil {
		return err
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	if err == nil {
		err = l.f.Sync()
	}
	if err != nil {
		// The file may be gone or full; start over with a fresh handle.
		_ = l.f.Close()
		l.f = nil
		return fmt.Errorf("audit: write: %w", err)
	}
	return nil
}

// ensureOpen leaves l.f open on the live audit.log, rotated when adding n
// bytes would pass the cap. A file deleted or replaced under the open handle
// is reopened, so entries never go to an unlinked inode.
func (l *Log) ensureOpen(n int64) error {
	path := filepath.Join(l.dir, File)
	if l.f != nil {
		cur, errCur := l.f.Stat()
		onDisk, errDisk := os.Stat(path)
		if errCur != nil || errDisk != nil || !os.SameFile(cur, onDisk) {
			_ = l.f.Close()
			l.f = nil
		}
	}
	if l.f != nil && l.size > 0 && l.size+n > l.maxBytes {
		if err := l.f.Close(); err != nil {
			l.f = nil
			return fmt.Errorf("audit: close for rotation: %w", err)
		}
		l.f = nil
		if err := rotate(l.dir); err != nil {
			return err
		}
	}
	if l.f != nil {
		return nil
	}
	f, size, err := openAppend(path)
	if err != nil {
		return err
	}
	if size > 0 && size+n > l.maxBytes {
		_ = f.Close()
		if err := rotate(l.dir); err != nil {
			return err
		}
		if f, size, err = openAppend(path); err != nil {
			return err
		}
	}
	l.f, l.size = f, size
	return nil
}

// rotate shifts audit.log to .1 and .1 to .2; the old .2 is removed.
func rotate(dir string) error {
	base := filepath.Join(dir, File)
	for i := Files - 1; i >= 1; i-- {
		from := base
		if i > 1 {
			from = fmt.Sprintf("%s.%d", base, i-1)
		}
		to := fmt.Sprintf("%s.%d", base, i)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("audit: rotate: %w", err)
		}
	}
	return nil
}

// Close closes the file. Appends after Close open it again.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
