package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Journal is the native slice of agent.db the host drives. *ring.DB is one;
// the host never touches another table. Every mutation is one writer job;
// no journal call spans an adapter call.
type Journal interface {
	State(ctx context.Context) (ring.State, error)
	Command(ctx context.Context, id string) (ring.Entry, error)
	CreateNativeSession(ctx context.Context, r ring.NativeSessionRecord) (ring.NativeSessionRecord, error)
	ReadNativeSession(ctx context.Context, id string) (ring.NativeSessionRecord, error)
	BindNativeProviderSession(ctx context.Context, id, controller string, generation uint64, startEffectID, providerID string) (ring.NativeSessionRecord, error)
	AdmitNativeEffect(ctx context.Context, r ring.NativeEffectRecord) (ring.NativeEffectRecord, bool, error)
	ClaimNativeEffect(ctx context.Context, id, controller string, generation uint64) (ring.NativeEffectRecord, error)
	ResolveNativeEffect(ctx context.Context, id, controller string, generation uint64, outcome ring.NativeEffectState, evidence ring.NativeEvidence) (ring.NativeEffectRecord, error)
	RecoverNativeEffects(ctx context.Context, after string, limit int) ([]ring.NativeEffectRecord, error)
	ListUnresolvedNativeEffects(ctx context.Context, after string, limit int) ([]ring.NativeEffectRecord, error)
	ReadNativeEffect(ctx context.Context, id string) (ring.NativeEffectRecord, error)
}

// Emitter publishes classified native events. *events.Emitter is one: it
// stamps delivery and coalescing, keeps NEVER_DROP in the ring and drops
// SHED under pressure instead of blocking the caller.
type Emitter interface {
	Emit(ctx context.Context, ev *agentv1.Event) (uint64, error)
}

// Options build a Host. DB, MachineID and Clock are required; Emitter nil
// drops event publication (results still resolve); Adapters empty means no
// backend is qualified and every command is UNSUPPORTED.
type Options struct {
	DB        Journal
	MachineID string
	Home      string
	Emitter   Emitter
	Adapters  []Adapter
	Telemetry *telemetry.Telemetry
	Logger    *slog.Logger
	Now       func() time.Time
	// OrderWait bounds how long an ordinary effect waits for earlier
	// unresolved effects of its session before it gives up its own
	// admission. Zero means the default (30 s, under every command
	// deadline). Interrupt and stop never wait.
	OrderWait time.Duration
}

const defaultOrderWait = 30 * time.Second

// Host runs native commands against the journal and one qualified adapter
// per backend. One Host per agent; methods are safe for concurrent use (the
// journal serializes each decision, the adapter calls run outside it).
type Host struct {
	db       Journal
	machine  string
	home     string
	emit     Emitter
	adapters map[string]Adapter
	metrics  *metrics
	log      *slog.Logger
	now      func() time.Time
	wait     time.Duration
	mu       sync.Mutex
	// turns maps provider turn identities to journal admission orders per
	// session while this process lives; the order is the durable turn
	// number on the wire. Ephemeral: a restart forgets it.
	turns map[string]map[string]uint64
}

// New builds the host. An adapter with an empty backend name, a zero
// version or a duplicate backend is refused: capabilities must name exactly
// one qualified backend.
func New(opts Options) (*Host, error) {
	if opts.DB == nil || opts.MachineID == "" {
		return nil, fmt.Errorf("native: host needs a journal and a machine id")
	}
	adapters := map[string]Adapter{}
	for _, a := range opts.Adapters {
		if a == nil {
			return nil, fmt.Errorf("native: nil adapter")
		}
		name := backendName(a.Backend())
		if name == "" || a.Version() == 0 {
			return nil, fmt.Errorf("native: adapter with no qualified backend")
		}
		if adapters[name] != nil {
			return nil, fmt.Errorf("native: two adapters for %s", name)
		}
		adapters[name] = a
	}
	m, err := newMetrics(opts.Telemetry)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	wait := opts.OrderWait
	if wait <= 0 {
		wait = defaultOrderWait
	}
	return &Host{
		db: opts.DB, machine: opts.MachineID, home: opts.Home,
		emit: opts.Emitter, adapters: adapters,
		metrics: m, log: log, now: now, wait: wait,
	}, nil
}

// Advertised lists one capability per qualified backend. Empty until a
// backend passes ADR 0033 qualification: unimplemented adapters stay
// unadvertised, and the executor answers their commands UNSUPPORTED.
func (h *Host) Advertised() []*typev1.Capability {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.advertisedLocked()
}

func (h *Host) advertisedLocked() []*typev1.Capability {
	var out []*typev1.Capability
	for _, a := range h.adapters {
		out = append(out, &typev1.Capability{Name: "native." + backendName(a.Backend()), Version: a.Version()})
	}
	return out
}

// adapterFor returns the qualified adapter for a backend, or false when
// this backend cannot run here.
func (h *Host) adapterFor(b typev1.NativeBackend) (Adapter, bool) {
	name := backendName(b)
	if name == "" {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.adapters[name]
	return a, ok
}

// effectKindOf maps a command to its journal kind by the oneof field name
// (the wire spelling), or false for any other command.
func effectKindOf(cmd *agentv1.Command) (ring.NativeEffectKind, bool) {
	f := cmd.ProtoReflect().WhichOneof(cmd.ProtoReflect().Descriptor().Oneofs().ByName("kind"))
	if f == nil {
		return "", false
	}
	switch string(f.Name()) {
	case "start_native_session":
		return ring.NativeEffectStart, true
	case "send_native_turn":
		return ring.NativeEffectSendTurn, true
	case "resume_native_session":
		return ring.NativeEffectResume, true
	case "interrupt_native_session":
		return ring.NativeEffectInterrupt, true
	case "stop_native_session":
		return ring.NativeEffectStop, true
	default:
		return "", false
	}
}

// digestOf returns the hex SHA-256 of the exact logged command bytes, so
// the admission binds the input the executor saved, not a remarshal.
func (h *Host) digestOf(ctx context.Context, commandID string) (string, error) {
	ent, err := h.db.Command(ctx, commandID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(ent.Body)
	return hex.EncodeToString(sum[:]), nil
}

// noCallEvidence attests that this host instance authorized no external
// call for the effect: only its own abandoned PREPARED row may resolve
// with it, never a claimed effect that reached an adapter.
func noCallEvidence(effectID string) ring.NativeEvidence {
	sum := sha256.Sum256([]byte("native-no-call:" + effectID))
	return ring.NativeEvidence{Kind: ring.NativeEvidenceProviderAbsence, Digest: hex.EncodeToString(sum[:])}
}

// receiptEvidence digests the bounded non-secret receipt of a confirmed
// call. The receipt itself is never saved in agent.db.
func receiptEvidence(effectID, summary string) ring.NativeEvidence {
	sum := sha256.Sum256([]byte("native-receipt:" + effectID + ":" + summary))
	return ring.NativeEvidence{Kind: ring.NativeEvidenceProviderReceipt, Digest: hex.EncodeToString(sum[:])}
}

// Run executes one native command through admit, claim, one adapter call
// and resolve. It never runs for an unadvertised capability (the executor
// refuses those first); it checks again anyway and fails closed.
func (h *Host) Run(ctx context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
	kind, ok := effectKindOf(cmd)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: not a native command")
	}
	digest, err := h.digestOf(ctx, cmd.GetCommandId().GetValue())
	if err != nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the logged command is missing")
	}
	switch kind {
	case ring.NativeEffectStart:
		return h.runStart(ctx, cmd, digest)
	case ring.NativeEffectSendTurn:
		return h.runSend(ctx, cmd, digest)
	case ring.NativeEffectResume:
		return h.runResume(ctx, cmd, digest)
	case ring.NativeEffectInterrupt:
		return h.runInterrupt(ctx, cmd, digest)
	case ring.NativeEffectStop:
		return h.runStop(ctx, cmd, digest)
	default:
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: unknown effect kind")
	}
}

// fail is a FAILED result with a static message (never screen text).
func fail(reason typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_FAILED, Reason: reason, Message: msg}
}

// unknownOutcome is the only answer when the provider may have acted.
func unknownOutcome(msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_UNKNOWN_OUTCOME, Message: msg}
}

// admit binds one effect id (always the command id: one external effect per
// command) to the logged command. A duplicate admission returns the stored
// row; the caller then replays or reconciles, never duplicates.
func (h *Host) admit(ctx context.Context, cmd *agentv1.Command, sessionID string, generation uint64, kind ring.NativeEffectKind, digest string) (ring.NativeEffectRecord, bool, error) {
	return h.db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: cmd.GetCommandId().GetValue(), CommandID: cmd.GetCommandId().GetValue(),
		SessionID: sessionID, Generation: generation, Kind: kind, CommandBodyDigest: digest,
	})
}

// abandon resolves this attempt's own PREPARED effect as NO_EFFECT: the
// claim never committed, so no call was authorized. It re-reads the row and
// only resolves a still-PREPARED one: a stale snapshot must never turn a
// claimed effect into NO_EFFECT. It is only correct between admit and a
// successful claim, while this goroutine owns the attempt.
func (h *Host) abandon(ctx context.Context, effect ring.NativeEffectRecord) {
	if effect.State != ring.NativeEffectPrepared {
		return
	}
	current, err := h.db.ReadNativeEffect(ctx, effect.EffectID)
	if err != nil || current.State != ring.NativeEffectPrepared {
		return
	}
	if _, err := h.db.ResolveNativeEffect(ctx, effect.EffectID, h.machine, effect.Generation,
		ring.NativeEffectNoEffect, noCallEvidence(effect.EffectID)); err != nil {
		h.log.InfoContext(ctx, "native.effect_abandon_failed",
			"effect", effect.EffectID, "command_id", effect.CommandID, "resolve_err", err.Error())
		return
	}
	h.metrics.resolved(string(effect.Kind), string(ring.NativeEffectNoEffect))
	logResolvedLine(h.log, string(effect.Kind), string(ring.NativeEffectNoEffect),
		effect.CommandID, effect.SessionID, effect.EffectID, "abandoned before claim")
}

// claim waits for earlier unresolved effects of the session in admission
// order, then commits MAY_HAVE_RUN. Interrupt and stop bypass the wait but
// keep every other check. Giving up cleans this attempt's own admission so
// the session stays live; a held session reports HELD instead of busy.
func (h *Host) claim(ctx context.Context, effect ring.NativeEffectRecord, urgent bool) (ring.NativeEffectRecord, *agentv1.CommandResult) {
	if urgent {
		return h.claimOnce(ctx, effect)
	}
	deadline := h.now().Add(h.wait)
	for {
		got, refusal := h.claimOnce(ctx, effect)
		if refusal == nil {
			return got, nil
		}
		if refusal.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_TURN_BUSY {
			return ring.NativeEffectRecord{}, refusal
		}
		if !h.now().Before(deadline) {
			h.abandon(ctx, effect)
			if h.isHeld(ctx, effect.SessionID) {
				return ring.NativeEffectRecord{}, held(effect.SessionID)
			}
			return ring.NativeEffectRecord{}, busy(effect.SessionID)
		}
		select {
		case <-ctx.Done():
			h.abandon(ctx, effect)
			return ring.NativeEffectRecord{}, unknownOutcome("native: cancelled while waiting for the session's turn")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (h *Host) claimOnce(ctx context.Context, effect ring.NativeEffectRecord) (ring.NativeEffectRecord, *agentv1.CommandResult) {
	got, err := h.db.ClaimNativeEffect(ctx, effect.EffectID, h.machine, effect.Generation)
	switch {
	case err == nil:
		return got, nil
	case errors.Is(err, ring.ErrNativeOrder):
		return ring.NativeEffectRecord{}, busy(effect.SessionID)
	case errors.Is(err, ring.ErrNativeHeld):
		return ring.NativeEffectRecord{}, held(effect.SessionID)
	case errors.Is(err, ring.ErrNativeOwner):
		h.abandon(ctx, effect)
		return ring.NativeEffectRecord{}, stale(effect.SessionID)
	default:
		h.abandon(ctx, effect)
		return ring.NativeEffectRecord{}, fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the claim did not commit")
	}
}

// held reports a session held for reconciliation; busy an ordinary ordering wait.
func held(sessionID string) *agentv1.CommandResult {
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_FAILED,
		Reason:  typev1.ReasonCode_REASON_CODE_NATIVE_SESSION_HELD,
		Message: "native: session " + sessionID + " is held for reconciliation",
	}
}

func busy(sessionID string) *agentv1.CommandResult {
	r := &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_FAILED,
		Reason:  typev1.ReasonCode_REASON_CODE_NATIVE_TURN_BUSY,
		Message: "native: session " + sessionID + " still has an unresolved turn",
	}
	r.Retryable = true
	return r
}

func stale(sessionID string) *agentv1.CommandResult {
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_FAILED,
		Reason:  typev1.ReasonCode_REASON_CODE_NATIVE_GENERATION_STALE,
		Message: "native: session " + sessionID + " moved to a newer generation",
	}
}

// isHeld checks the session's control state without changing it.
func (h *Host) isHeld(ctx context.Context, sessionID string) bool {
	s, err := h.db.ReadNativeSession(ctx, sessionID)
	return err == nil && s.ControlState == ring.NativeControlHeld
}

// reenter replays a duplicate admission (same command id, no logged result:
// a crash between resolve and the result write). Terminal effects rebuild
// their result; anything else is uncertain, never a duplicate call.
func (h *Host) reenter(ctx context.Context, effect ring.NativeEffectRecord, kind ring.NativeEffectKind) *agentv1.CommandResult {
	switch effect.State {
	case ring.NativeEffectConfirmed:
		return h.confirmedResult(ctx, effect, kind)
	case ring.NativeEffectNoEffect:
		if effect.EvidenceKind == ring.NativeEvidencePrelaunch {
			r := fail(typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED, "native: the backend is not qualified on this machine")
			r.Retryable = true
			return r
		}
		return unknownOutcome("native: the turn never ran; reconcile before retrying with fresh ids")
	default:
		return unknownOutcome("native: the effect is already in flight or held; reconcile before retrying")
	}
}

// Recover runs at agent start, before dispatch: PREPARED effects belong to
// dead attempts (no live holder exists yet), so they resolve NO_EFFECT with
// absence proof; then every MAY_HAVE_RUN becomes RECONCILE_REQUIRED and its
// session is held, in bounded pages. Recovery first suppresses dispatch
// from old receipts: the executor's resume pass ends running native commands
// UNKNOWN_OUTCOME without calling handlers.
func (h *Host) Recover(ctx context.Context) error {
	unresolved, err := h.db.ListUnresolvedNativeEffects(ctx, "", 1000)
	if err != nil {
		return fmt.Errorf("native: list unresolved: %w", err)
	}
	for _, e := range unresolved {
		if e.State != ring.NativeEffectPrepared {
			continue
		}
		if _, err := h.db.ResolveNativeEffect(ctx, e.EffectID, h.machine, e.Generation,
			ring.NativeEffectNoEffect, noCallEvidence(e.EffectID)); err != nil {
			return fmt.Errorf("native: abandon prepared %s: %w", e.EffectID, err)
		}
		h.metrics.resolved(string(e.Kind), string(ring.NativeEffectNoEffect))
		logResolvedLine(h.log, string(e.Kind), string(ring.NativeEffectNoEffect),
			e.CommandID, e.SessionID, e.EffectID, "dead attempt cleaned at startup")
	}
	var heldCount, recovered int
	for cursor := ""; ; {
		page, err := h.db.RecoverNativeEffects(ctx, cursor, 1000)
		if err != nil {
			return fmt.Errorf("native: recover: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if e.State == ring.NativeEffectReconcileRequired {
				recovered++
				if h.isHeld(ctx, e.SessionID) {
					heldCount++
				}
			}
		}
		cursor = page[len(page)-1].EffectID
		if len(page) < 1000 {
			break
		}
	}
	h.log.InfoContext(ctx, "native.recovery_completed", "reconcile_required", recovered, "sessions_held", heldCount)
	return nil
}

// workRef derives the journal's logical workdir reference: deterministic,
// alphabet-safe ([a-z0-9-]) and collision-practical, never a path.
func workRef(repoHost, repoPath, subpath string) string {
	sum := sha256.Sum256([]byte(repoHost + "\x00" + repoPath + "\x00" + subpath))
	return "wrk-" + hex.EncodeToString(sum[:])[:28]
}

// validSubpath rejects absolute paths, parent escapes and empty segments:
// the adapter joins the subpath to an allowed checkout, never to a hub path.
func validSubpath(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 256 || s[0] == '/' {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// refuse maps journal and validation errors of a fresh attempt (nothing
// claimed yet) to results. The caller abandons its own admission first.
func (h *Host) refuse(ctx context.Context, effect ring.NativeEffectRecord, err error) *agentv1.CommandResult {
	h.abandon(ctx, effect)
	switch {
	case errors.Is(err, ring.ErrNativeUnknownSession):
		return fail(typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND, "native: no such managed session")
	case errors.Is(err, ring.ErrNativeOwner):
		return stale(effect.SessionID)
	case errors.Is(err, ring.ErrNativeHeld):
		return held(effect.SessionID)
	case errors.Is(err, ring.ErrNativeConflict):
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the effect conflicts with a stored one")
	default:
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the admission did not commit")
	}
}
