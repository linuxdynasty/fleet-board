package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Store keeps the held settings in agent.db; *ring.DB is one.
type Store interface {
	HeldSettings(ctx context.Context) (ring.Held, bool, error)
	SaveSettings(ctx context.Context, h ring.Held) error
}

// Local is the policy file in force; *policy.Manager is one.
type Local interface {
	Policy() *policy.Policy
	SafeMode() bool
	Info() *agentv1.PolicyInfo
	// Recheck re-hashes the file (the safety net of ruling row 6 fork 7).
	Recheck(ctx context.Context)
}

// Sender queues the SettingsAck for the connected stream; *conn.Loop is one.
type Sender interface {
	Send(ctx context.Context, m *agentv1.ConnectRequest) error
}

// Options are the inputs of [Open]. DB, Local and Sender are needed.
type Options struct {
	DB        Store
	Local     Local
	Sender    Sender
	Telemetry *telemetry.Telemetry
	// Logger has component and machine_id set; default Telemetry.Logger.
	Logger *slog.Logger
	// Now stamps the held settings (agent clock); nil is time.Now.
	Now func() time.Time
	// Intake is the room of the intake; zero is DefaultIntake.
	Intake int
}

// DefaultIntake is how many Settings wait for the worker before the hook
// refuses more (command.intake_full, ruling row 6 fork 12 c).
const DefaultIntake = 8

// Manager holds the effective settings: the hub's held settings merged
// tighten-only under the policy file (03e W10, L2).
//
// Goroutines: the connection loop's sending goroutine calls Hook, which
// only puts the message in the intake; Run's one goroutine takes it, merges,
// saves it in agent.db (one store job) and then queues the SettingsAck. The
// effective values are one immutable view behind an atomic pointer, swapped
// by Run after the save, so Snapshot and the executor never wait.
type Manager struct {
	opts Options
	log  *slog.Logger
	m    *metrics
	now  func() time.Time
	in   chan item
	cur  atomic.Pointer[view]
}

// item is one Settings from the hub with the context of its stream.
type item struct {
	ctx context.Context
	s   *agentv1.Settings
}

// view is the held settings and what the executor reads of them.
type view struct {
	held     bool
	epoch    string
	gen      uint64
	kept     *agentv1.Settings
	ack      *agentv1.SettingsAck
	rates    map[string]int
	approval map[string]bool
}

// Open reads the held settings from agent.db and merges them under the
// policy file loaded now, which may differ from the one they were merged
// under before the restart: the held ack is then saved again with the new
// merge (settings.reevaluated). Before any Settings, and in safe mode, the
// built-in defaults are in effect at generation 0; safe mode saves nothing.
func Open(ctx context.Context, opts Options) (*Manager, error) {
	if opts.DB == nil || opts.Local == nil || opts.Sender == nil {
		return nil, errors.New("settings: DB, Local and Sender are needed")
	}
	m := &Manager{opts: opts, log: opts.Logger, now: opts.Now}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil && opts.Telemetry != nil {
		m.log = opts.Telemetry.Logger.With("component", "settings")
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	n := opts.Intake
	if n <= 0 {
		n = DefaultIntake
	}
	m.in = make(chan item, n)
	held, ok, err := opts.DB.HeldSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: read the held settings: %w", err)
	}
	kept, was := &agentv1.Settings{}, &agentv1.SettingsAck{}
	if ok {
		if err := proto.Unmarshal(held.Settings, kept); err != nil {
			return nil, fmt.Errorf("settings: decode the held settings: %w", err)
		}
		if err := proto.Unmarshal(held.Ack, was); err != nil {
			return nil, fmt.Errorf("settings: decode the held ack: %w", err)
		}
	}
	safe := opts.Local.SafeMode()
	out := Merge(Input{Hub: kept, Local: opts.Local.Policy(), SafeMode: safe})
	carry(out.Ack, was.GetRejected(), safe)
	m.cur.Store(newView(ok, kept, out.Ack)) // before the gauge can read it
	if m.m, err = newMetrics(opts.Telemetry, m.generation); err != nil {
		return nil, fmt.Errorf("settings: register metrics: %w", err)
	}
	if ok && !safe { // safe mode saves nothing (03e2)
		if err := m.reevaluate(ctx, held, out); err != nil {
			m.m.close() // no Manager is returned to Close
			return nil, err
		}
	}
	return m, nil
}

// carry puts the saved ack's rejections back in ack at start. agent.db keeps
// the message with each invalid field already replaced, so merging it again
// finds nothing to reject; but no check reads the policy file, so the saved
// list still holds and the hub must go on seeing it (03e failure table). The
// saved list comes first, in its order, then any field the merge rejects now
// that it does not name (a newer binary's stricter check). In safe mode
// every field is rejected for safe mode, the saved ones included.
func carry(ack *agentv1.SettingsAck, saved []*typev1.SettingRejection, safe bool) {
	named := map[string]bool{}
	var out []*typev1.SettingRejection
	add := func(r *typev1.SettingRejection) {
		if !named[r.GetField()] {
			named[r.GetField()] = true
			out = append(out, r)
		}
	}
	if safe {
		for _, r := range ack.GetRejected() {
			add(r)
		}
		for _, r := range saved {
			add(&typev1.SettingRejection{Field: r.GetField(), Reason: typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE, Message: safeModeMsg})
		}
	} else {
		for _, r := range saved {
			add(r)
		}
		for _, r := range ack.GetRejected() {
			add(r)
		}
	}
	ack.Rejected = out
}

// generation is the held generation for the gauge (0 before any).
func (m *Manager) generation() int64 {
	if v := m.cur.Load(); v != nil {
		return int64(min(v.gen, 1<<62))
	}
	return 0
}

// reevaluate saves the merge when the policy file changed since the save.
// Under the same file the saved ack stands: the merge again gives the same
// ack with the same binary, and a newer binary's stricter checks are found
// again at each start (they reach the view and the repeat answer).
func (m *Manager) reevaluate(ctx context.Context, held ring.Held, out Outcome) error {
	hash := m.opts.Local.Info().GetHash()
	if hash == held.PolicyHash {
		return nil
	}
	ack, err := marshal(out.Ack)
	if err != nil {
		return err
	}
	old := held.PolicyHash
	held.Ack, held.PolicyHash, held.Applied = ack, hash, m.now()
	if err := m.opts.DB.SaveSettings(ctx, held); err != nil {
		return fmt.Errorf("settings: save the merge under the new policy: %w", err)
	}
	m.log.InfoContext(ctx, "settings.reevaluated", "old_hash", old, "new_hash", hash,
		"generation", held.Generation, "capped", len(out.Ack.GetCappedFields()))
	return nil
}

func newView(held bool, kept *agentv1.Settings, ack *agentv1.SettingsAck) *view {
	v := &view{
		held: held, epoch: kept.GetSettingsEpoch(), gen: kept.GetGeneration(), kept: kept, ack: ack,
		rates: map[string]int{}, approval: map[string]bool{},
	}
	for _, r := range ack.GetEffective().GetValues().GetRateLimits() {
		v.rates[r.GetCommandKind()] = int(r.GetPerMinute())
	}
	for _, r := range ack.GetEffective().GetValues().GetLocalApproval() {
		v.approval[r.GetCommandKind()] = r.GetRequired()
	}
	return v
}

// Hook is conn.LoopOptions.Settings: it puts s in the intake and never
// waits. False means the intake is full (the loop logs command.intake_full
// and the hub sends Settings again on the next connect).
func (m *Manager) Hook(ctx context.Context, _ uint64, s *agentv1.Settings) bool {
	select {
	case m.in <- item{ctx: ctx, s: s}:
		return true
	default:
		return false
	}
}

// Run takes each Settings from the intake, in order, until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case it := <-m.in:
			m.handle(ctx, it)
		}
	}
}

// Close releases the gauge callback; call it after Run returned.
func (m *Manager) Close() { m.m.close() }

// Effective is a copy of the effective settings.
func (m *Manager) Effective() *typev1.EffectiveSettings {
	e, _ := proto.Clone(m.cur.Load().ack.GetEffective()).(*typev1.EffectiveSettings)
	return e
}

// Snapshot fills Snapshot.settings with the effective settings.
func (m *Manager) Snapshot(_ context.Context, s *agentv1.Snapshot) error {
	s.Settings = m.Effective()
	return nil
}

// SafeMode is policy safe mode (exec.Policy).
func (m *Manager) SafeMode() bool { return m.opts.Local.SafeMode() }

// RateLimit is the effective per-minute limit of kind; ok is false when no
// layer states one (exec.Policy).
func (m *Manager) RateLimit(kind string) (int, bool) {
	n, ok := m.cur.Load().rates[kind]
	return n, ok
}

// ApprovalRequired reports an effective local_approval rule for kind
// (exec.Policy).
func (m *Manager) ApprovalRequired(kind string) bool { return m.cur.Load().approval[kind] }

func marshal(m proto.Message) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("settings: encode: %w", err)
	}
	return b, nil
}
