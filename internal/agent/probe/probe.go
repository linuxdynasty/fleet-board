package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// probeServer is the PaneKey server name of the hidden probe server.
const probeServer = "fleet-probe"

// defaultInterval is the probe cadence when EnsureProbe carries none.
const defaultInterval = 10 * time.Minute

// harnessBinaries maps a probed harness to its program on PATH. OpenCode
// is absent on purpose: it takes no probe session.
var harnessBinaries = map[string]string{
	"claude": "claude",
	"codex":  "codex",
	"muse":   "muse",
	"cursor": "cursor-agent",
}

// shellCommands are pane commands that mean the harness is gone and a
// shell is left: the pass reports PROBE_START_FAILED and the next pass
// starts over. Anything else counts as the harness (or its child) alive.
var shellCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
}

// Emitter is where a pass reports ProbeHealth and UsageReading events.
// *events.Emitter is one; tests use a fake.
type Emitter interface {
	Emit(ctx context.Context, ev *agentv1.Event) (uint64, error)
}

// Options builds a Manager.
type Options struct {
	// Backend runs tmux on the fleet-probe server. Required.
	Backend Backend
	// Events takes the ProbeHealth and UsageReading events. Required.
	Events Emitter
	// ProbeDir is the fleet-owned C3 folder every probe session starts
	// in. Required: a probe never starts anywhere else.
	ProbeDir string
	// Now is the agent clock for read times and the schedule. Nil is
	// time.Now; tests pin it.
	Now func() time.Time
	// LookPath resolves a harness binary on PATH. Required: depguard
	// keeps os/exec in internal/proc, so the wiring row supplies PATH
	// resolution with the manager (a proc.LookPath helper is the
	// natural home); tests fake it.
	LookPath func(string) (string, error)
	// EnsureTries is the session-respawn attempts per pass (03b: 3).
	EnsureTries int
	// EnsureRetryEvery is the wait between respawns (03b: 30 s).
	EnsureRetryEvery time.Duration
	// SettlePolls and SettleEvery bound the wait for a stable screen.
	SettlePolls int
	SettleEvery time.Duration
	// DefaultInterval applies when EnsureProbe carries none.
	DefaultInterval time.Duration
}

// assignment is one probe the manager owns.
type assignment struct {
	probeID    *typev1.ProbeId
	accountID  *typev1.AccountId
	accountKey *typev1.AccountKey
	harness    *typev1.Harness
	interval   time.Duration
	nextRun    time.Time

	consecutive uint32
	lastSuccess *timestamppb.Timestamp
	lastFailure typev1.ReasonCode
}

// request is one handler call waiting on the manager goroutine.
type request struct {
	ensure *agentv1.EnsureProbe
	stop   *agentv1.StopProbe
	reply  chan *agentv1.CommandResult
}

// Manager owns every probe decision on one goroutine: assignments,
// passes, and the schedule all run in Run's loop, so two passes never
// overlap and no lock guards the table. Handlers are thin adapters that
// send a request and wait for its reply.
type Manager struct {
	be       Backend
	em       Emitter
	dir      string
	now      func() time.Time
	lookPath func(string) (string, error)

	ensureTries int
	ensureEvery time.Duration
	settleN     int
	settleEvery time.Duration
	defInterval time.Duration

	reqs chan request
}

// NewManager checks the setup and fails fast.
func NewManager(o Options) (*Manager, error) {
	if o.Backend == nil {
		return nil, errors.New("probe: Backend is required")
	}
	if o.Events == nil {
		return nil, errors.New("probe: Events is required")
	}
	if o.ProbeDir == "" {
		return nil, errors.New("probe: ProbeDir is required (a probe starts nowhere else)")
	}
	m := &Manager{
		be: o.Backend, em: o.Events, dir: o.ProbeDir,
		now: o.Now, lookPath: o.LookPath,
		ensureTries: o.EnsureTries, ensureEvery: o.EnsureRetryEvery,
		settleN: o.SettlePolls, settleEvery: o.SettleEvery,
		defInterval: o.DefaultInterval,
		reqs:        make(chan request),
	}
	if m.now == nil {
		m.now = time.Now
	}
	if o.LookPath == nil {
		return nil, errors.New("probe: LookPath is required")
	}
	if m.ensureTries <= 0 {
		m.ensureTries = 3
	}
	if m.ensureEvery <= 0 {
		m.ensureEvery = 30 * time.Second
	}
	if m.settleN <= 0 {
		m.settleN = 4
	}
	if m.settleEvery <= 0 {
		m.settleEvery = 500 * time.Millisecond
	}
	if m.defInterval <= 0 {
		m.defInterval = defaultInterval
	}
	return m, nil
}

// Run owns the manager goroutine: requests and the earliest due probe,
// one at a time, until ctx ends. Every pass runs here, never on a
// command goroutine, so the probe decision has exactly one owner.
func (m *Manager) Run(ctx context.Context) error {
	rows := map[string]*assignment{}
	var tick <-chan time.Time
	arm := func() {
		var earliest time.Time
		for _, a := range rows {
			if earliest.IsZero() || a.nextRun.Before(earliest) {
				earliest = a.nextRun
			}
		}
		if earliest.IsZero() {
			tick = nil
			return
		}
		// The manager clock decides due, not the wall clock, so a
		// pinned clock in tests never fires stray passes.
		wait := earliest.Sub(m.now())
		if wait < 0 {
			wait = 0
		}
		tick = time.After(wait)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-m.reqs:
			m.handle(ctx, rows, r)
			arm()
		case <-tick:
			now := m.now()
			for _, a := range rows {
				if !now.Before(a.nextRun) {
					// The schedule advances before the pass, so the
					// health it emits already names the next run.
					a.nextRun = m.now().Add(a.interval)
					m.pass(ctx, a)
				}
			}
			arm()
		}
	}
}

// sessionName derives the tmux session from the probe id: deterministic
// across agent restarts (no map to lose), fixed length, and practically
// collision-free, so StopProbe always finds its session.
func sessionName(probeID string) string {
	sum := sha256.Sum256([]byte(probeID))
	return "probe-" + hex.EncodeToString(sum[:])[:12]
}

// handle answers one handler call on the manager goroutine.
func (m *Manager) handle(ctx context.Context, rows map[string]*assignment, r request) {
	if r.stop != nil {
		r.reply <- m.stop(ctx, rows, r.stop)
		return
	}
	r.reply <- m.ensure(ctx, rows, r.ensure)
}

// ensure records the assignment and runs one pass when asked. It is
// declarative: a re-sent command re-records the same row, and only
// run_now runs a pass (once per distinct command_id, by the executor's
// command log above this handler).
func (m *Manager) ensure(ctx context.Context, rows map[string]*assignment, cmd *agentv1.EnsureProbe) *agentv1.CommandResult {
	if cmd == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: empty EnsureProbe")
	}
	id := cmd.GetProbeId().GetValue()
	harnessName := cmd.GetHarness().GetName()
	if id == "" || cmd.GetAccountId().GetValue() == "" || cmd.GetAccountKey().GetValue() == "" {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, "probe: probe id, account id and account key are required")
	}
	if _, ok := StatusCommand(harnessName); !ok {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, "probe: harness "+harnessName+" takes no probe session")
	}
	if cmd.GetProfile() != "" {
		return fail(typev1.ReasonCode_REASON_CODE_HARNESS_PROFILE_MISSING, false, "probe: harness profiles land in a later slice")
	}
	interval := m.defInterval
	if iv := cmd.GetInterval(); iv != nil {
		interval = iv.AsDuration()
	}
	if interval <= 0 {
		interval = m.defInterval
	}
	a := rows[id]
	if a == nil {
		a = &assignment{nextRun: m.now().Add(interval)}
		rows[id] = a
	} else if a.interval != interval {
		a.nextRun = m.now().Add(interval)
	}
	a.probeID, a.accountID, a.accountKey, a.harness, a.interval =
		cmd.GetProbeId(), cmd.GetAccountId(), cmd.GetAccountKey(), cmd.GetHarness(), interval
	if !cmd.GetRunNow() {
		return ok("probe " + id + " kept; next run carries the schedule")
	}
	return m.pass(ctx, a)
}

// stop tears the probe session down and drops the row. A missing session
// or a missing row is still OK: StopProbe is declarative.
func (m *Manager) stop(ctx context.Context, rows map[string]*assignment, cmd *agentv1.StopProbe) *agentv1.CommandResult {
	if cmd == nil || cmd.GetProbeId().GetValue() == "" {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false, "probe: probe id is required")
	}
	id := cmd.GetProbeId().GetValue()
	if err := m.be.KillSession(ctx, sessionName(id)); err != nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: the probe session would not stop")
	}
	delete(rows, id)
	return ok("probe " + id + " stopped")
}

// fail builds a FAILED result with a fixed message.
func fail(code typev1.ReasonCode, retryable bool, message string) *agentv1.CommandResult {
	return &agentv1.CommandResult{
		Status:    typev1.Status_STATUS_FAILED,
		Reason:    code,
		Message:   message,
		Retryable: retryable,
	}
}

// ok builds an OK result with a fixed message.
func ok(message string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_OK, Message: message}
}
