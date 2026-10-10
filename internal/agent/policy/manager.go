package policy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// DefaultSettle is how long the watcher waits after the last event naming
// policy.yaml before it re-hashes the file (defaults.md).
const DefaultSettle = 500 * time.Millisecond

// Auditor takes the local audit rows ([audit.Log] is one).
type Auditor interface{ Append(audit.Entry) }

// Change is a hash change of policy.yaml seen while the agent runs: the seam
// row 7's policy_changed event reads (02i, 03e).
type Change struct {
	// PreviousHash is the hash last seen on disk ("" when there was no file).
	PreviousHash string
	// Hash is the hash now on disk ("" when the file is gone).
	Hash string
	Time time.Time
	// Info is the PolicyInfo for the PolicyChanged event: the new on-disk
	// hash and changed_since_load. Snapshot.policy carries the loaded hash.
	Info *agentv1.PolicyInfo
}

// Options are the inputs of [Load].
type Options struct {
	// Dir is the config folder that holds policy.yaml.
	Dir       string
	Telemetry *telemetry.Telemetry
	// Logger is the logger to use, with its component and machine_id already
	// set; the default is Telemetry.Logger with component "policy".
	Logger *slog.Logger
	// Audit receives policy.safe_mode and policy.changed rows; nil drops them.
	Audit Auditor
	// OnChange is called for each hash change, in order, on a goroutine of
	// the Manager's own with no lock held, after the change is counted, logged
	// and audited. Recheck and Snapshot never wait for it, but later changes
	// queue behind a slow one, so it should return quickly (enqueue, do not
	// wait). Close waits for it to return.
	OnChange func(Change)
	// Settle is the watcher's settle time; zero is [DefaultSettle].
	Settle time.Duration
	// Now replaces time.Now for loaded_time and Change.Time; tests only.
	Now func() time.Time
	// NewWatcher builds the directory watcher; nil is fsnotify. Tests only.
	NewWatcher func() (Watcher, error)
}

// Manager holds the policy the agent loaded and watches the file for change.
//
// Locks, in order: checkMu (one re-hash at a time: read, hash, compare, queue
// the change) then mu (the small state below, held only to read or swap it).
// A change is reported (counter, log, audit fsync, OnChange) afterwards by one
// drain goroutine in order, with neither lock held, so neither Info nor a
// Snapshot waits for an audit sync or for OnChange.
type Manager struct {
	opts Options
	log  *slog.Logger
	path string
	m    *metrics
	now  func() time.Time

	// loaded is the policy in force: parsed once at Load, never replaced
	// (a change takes effect at the next restart, 03e).
	loaded *Policy
	// st is guarded by mu.
	mu sync.RWMutex
	st state

	checkMu sync.Mutex
	// pending and reporting are guarded by mu: the changes found and not yet
	// reported, and whether a drain goroutine is running.
	pending   []Change
	reporting bool
	wg        sync.WaitGroup // the drain goroutine

	watching atomic.Bool
	watchOff atomic.Bool
}

// state is what PolicyInfo is made from.
type state struct {
	loadedHash string
	schema     string
	loadedTime time.Time
	safeMode   bool
	problem    *Problem
	// diskHash is the hash last seen on disk.
	diskHash string
}

// Load reads policy.yaml once and returns the Manager. A missing file is a
// normal first run; an invalid or unreadable one is safe mode (a Manager is
// still returned). Load fails only when its own instruments cannot be made.
func Load(opts Options) (*Manager, error) {
	if opts.Telemetry == nil {
		return nil, errors.New("policy: Telemetry is required")
	}
	m := &Manager{opts: opts, path: filepath.Join(opts.Dir, File), now: opts.Now, log: opts.Logger}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = opts.Telemetry.Logger.With("component", "policy")
	}
	var err error
	if m.m, err = newMetrics(opts.Telemetry); err != nil {
		return nil, fmt.Errorf("policy: register metrics: %w", err)
	}
	m.load()
	return m, nil
}

func (m *Manager) load() {
	fr, rerr := readFile(m.path)
	st := state{loadedTime: m.now(), loadedHash: fr.hash, diskHash: fr.hash}
	m.loaded = &Policy{}
	switch {
	case fr.problem != nil:
		st.safeMode, st.problem = true, fr.problem
		if rerr != nil {
			m.log.Warn("policy.check_failed", "err_kind", "io")
		}
	case fr.present:
		res := Parse(fr.data)
		st.schema = res.SchemaVersion
		if res.Problem != nil {
			st.safeMode, st.problem = true, res.Problem
		} else {
			m.loaded = res.Policy
		}
	}
	m.st = st
	if st.safeMode {
		m.m.safeMode.Store(1)
		m.log.Warn("policy.safe_mode", "error", st.problem.Error(), "code", st.problem.Code)
		m.audit(audit.Entry{Action: audit.PolicySafeMode, Reason: st.problem.Code, Detail: map[string]string{"hash": st.loadedHash}})
	}
	m.log.Info("policy.loaded", "hash", st.loadedHash, "schema_version", st.schema, "safe_mode", st.safeMode)
	if m.loaded.ExecRawIgnored {
		m.log.Info("policy.key_ignored", "key", "exec.raw")
	}
}

func (m *Manager) audit(e audit.Entry) {
	if m.opts.Audit != nil {
		m.opts.Audit.Append(e)
	}
}

// Policy is the policy in force. In safe mode it is empty (no local opinion,
// no roots); callers check [Manager.SafeMode] before they act on it.
func (m *Manager) Policy() *Policy { return m.loaded }

// SafeMode reports policy safe mode: observe only, every control command
// and every hub setting refused (03e).
func (m *Manager) SafeMode() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st.safeMode
}

// Info is the PolicyInfo of a Snapshot: the loaded file's hash and schema
// version, when it was loaded, whether the file changed on disk since, and
// safe mode with its one-line reason.
func (m *Manager) Info() *agentv1.PolicyInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.infoLocked(m.st.loadedHash)
}

func (m *Manager) infoLocked(hash string) *agentv1.PolicyInfo {
	info := &agentv1.PolicyInfo{
		SchemaVersion: m.st.schema, Hash: hash, LoadedTime: timestamppb.New(m.st.loadedTime),
		ChangedSinceLoad: m.st.diskHash != m.st.loadedHash, SafeMode: m.st.safeMode,
	}
	if m.st.safeMode {
		info.Error = m.st.problem.Error()
	}
	return info
}

// Snapshot is the Snapshot hook for policy: it re-hashes the file (the
// event-driven safety net when no watcher could be set), then fills
// Snapshot.policy.
func (m *Manager) Snapshot(ctx context.Context, s *agentv1.Snapshot) error {
	m.Recheck(ctx)
	s.Policy = m.Info()
	return nil
}

// Recheck re-hashes policy.yaml now. A hash different from the last one seen
// is a change: it is counted, logged, audited and passed to OnChange, in order,
// by a goroutine of the Manager's own; Recheck returns without waiting for
// that. A file that cannot be read changes nothing and is logged. Safe to call
// from any goroutine; re-hashes run one at a time.
func (m *Manager) Recheck(ctx context.Context) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	fr, err := readFile(m.path)
	if err != nil {
		m.log.WarnContext(ctx, "policy.check_failed", "err_kind", "io")
		return
	}
	m.mu.Lock()
	prev := m.st.diskHash
	if fr.hash == prev {
		m.mu.Unlock()
		return
	}
	m.st.diskHash = fr.hash
	m.pending = append(m.pending, Change{PreviousHash: prev, Hash: fr.hash, Time: m.now(), Info: m.infoLocked(fr.hash)})
	start := !m.reporting
	m.reporting = true
	m.mu.Unlock()
	if start {
		m.wg.Go(m.drain)
	}
}

// drain reports the queued changes in order until none is left.
func (m *Manager) drain() {
	for {
		m.mu.Lock()
		if len(m.pending) == 0 {
			m.reporting = false
			m.mu.Unlock()
			return
		}
		c := m.pending[0]
		m.pending = m.pending[1:]
		m.mu.Unlock()
		m.report(c)
	}
}

func (m *Manager) report(c Change) {
	ctx := context.Background()
	m.m.changed.Add(ctx, 1)
	m.log.InfoContext(ctx, "policy.changed", "old_hash", c.PreviousHash, "new_hash", c.Hash)
	m.audit(audit.Entry{Time: c.Time, Action: audit.PolicyChanged, Detail: map[string]string{"old_hash": c.PreviousHash, "new_hash": c.Hash}})
	if m.opts.OnChange != nil {
		m.opts.OnChange(c)
	}
}

// Watching reports whether the directory watch is set.
func (m *Manager) Watching() bool { return m.watching.Load() }

// WatchOff reports that the directory watch could not be set (or was lost);
// status.json carries policy_watch: off. The file is still re-hashed at each
// Snapshot and Settings message.
func (m *Manager) WatchOff() bool { return m.watchOff.Load() }

// Close waits for the changes already found to be reported, then releases the
// gauge callback. Stop the Watch goroutine and the callers of Recheck first.
func (m *Manager) Close() {
	m.wg.Wait()
	_ = m.m.reg.Unregister()
}
