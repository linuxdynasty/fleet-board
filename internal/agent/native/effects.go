package native

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Session-id and reference bounds (ids.proto: hub ids are [a-z0-9-] to 32).
func validSessionID(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func validRef(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for i := range len(s) {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}

// inputCap bounds one turn's operator text (the SendNativeTurn cap).
const inputCap = 32 << 10

// startSession reads the managed session for a non-start command and checks
// generation and control state. The journal owns the checks at claim time
// too; these early reads fail fast with the right reason.
func (h *Host) startSession(ctx context.Context, sessionID string, generation uint64, urgent bool) (ring.NativeSessionRecord, *agentv1.CommandResult) {
	s, err := h.db.ReadNativeSession(ctx, sessionID)
	switch {
	case err == nil:
		if s.Generation != generation {
			return ring.NativeSessionRecord{}, stale(sessionID)
		}
		if s.ControlState == ring.NativeControlHeld && !urgent {
			return ring.NativeSessionRecord{}, held(sessionID)
		}
		return s, nil
	case errors.Is(err, ring.ErrNativeUnknownSession):
		return ring.NativeSessionRecord{}, fail(typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND, "native: no such managed session")
	default:
		return ring.NativeSessionRecord{}, fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the session read failed")
	}
}

// runSend hands one turn to a managed session in admission order.
func (h *Host) runSend(ctx context.Context, cmd *agentv1.Command, digest string) *agentv1.CommandResult {
	body := cmd.GetSendNativeTurn()
	if body == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: send arm missing")
	}
	sessionID := body.GetSession().GetValue()
	if !validSessionID(sessionID) {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: the session id is invalid")
	}
	if len(body.GetInput()) == 0 || len(body.GetInput()) > inputCap {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: the turn input is empty or over 32 KiB")
	}
	session, refusal := h.startSession(ctx, sessionID, body.GetOwnershipGeneration(), false)
	if refusal != nil {
		return refusal
	}
	backend, ok := BackendOf(session.Harness)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED, "native: the session backend is unknown to this agent")
	}
	adapter, ok := h.adapterFor(backend)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED, "native: the backend is not qualified on this machine")
	}
	effect, dup, err := h.admit(ctx, cmd, sessionID, session.Generation, ring.NativeEffectSendTurn, digest)
	if err != nil {
		return h.refuse(ctx, effect, err)
	}
	if dup {
		return h.reenter(ctx, effect, ring.NativeEffectSendTurn)
	}
	claimed, refusal := h.claim(ctx, effect, false)
	if refusal != nil {
		return refusal
	}
	receipt, callErr := adapter.SendTurn(ctx, TurnSpec{
		SessionID: sessionID, ProviderSessionID: session.ProviderSessionID,
		Generation: session.Generation, Turn: claimed.AdmissionOrder, Input: body.GetInput(),
	})
	if callErr != nil {
		return h.callFailed(ctx, claimed, callErr)
	}
	h.noteTurn(sessionID, receipt.ProviderTurnID, claimed.AdmissionOrder)
	resolved, resErr := h.resolveConfirmed(ctx, claimed, receipt.ProviderTurnID, sessionID)
	if resErr != nil {
		return resErr
	}
	_ = resolved
	h.metrics.resolved(string(claimed.Kind), string(ring.NativeEffectConfirmed))
	logResolvedLine(h.log, string(claimed.Kind), string(ring.NativeEffectConfirmed),
		claimed.CommandID, sessionID, claimed.EffectID, "turn admitted")
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_SendNativeTurn{SendNativeTurn: &agentv1.SendNativeTurnResult{
			EffectId: claimed.EffectID, AdmissionOrder: claimed.AdmissionOrder,
		}},
	}
}

// runResume continues a session after its process ended or stream broke.
func (h *Host) runResume(ctx context.Context, cmd *agentv1.Command, digest string) *agentv1.CommandResult {
	body := cmd.GetResumeNativeSession()
	if body == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: resume arm missing")
	}
	sessionID := body.GetSession().GetValue()
	if !validSessionID(sessionID) {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: the session id is invalid")
	}
	session, refusal := h.startSession(ctx, sessionID, body.GetOwnershipGeneration(), false)
	if refusal != nil {
		return refusal
	}
	backend, ok := BackendOf(session.Harness)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED, "native: the session backend is unknown to this agent")
	}
	adapter, ok := h.adapterFor(backend)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED, "native: the backend is not qualified on this machine")
	}
	effect, dup, err := h.admit(ctx, cmd, sessionID, session.Generation, ring.NativeEffectResume, digest)
	if err != nil {
		return h.refuse(ctx, effect, err)
	}
	if dup {
		return h.reenter(ctx, effect, ring.NativeEffectResume)
	}
	claimed, refusal := h.claim(ctx, effect, false)
	if refusal != nil {
		return refusal
	}
	receipt, callErr := adapter.Resume(ctx, ResumeSpec{
		SessionID: sessionID, ProviderSessionID: session.ProviderSessionID, Generation: session.Generation,
	})
	if callErr != nil {
		return h.callFailed(ctx, claimed, callErr)
	}
	providerID := session.ProviderSessionID
	if !receipt.ProviderSessionKept {
		if !validRef(receipt.ProviderSessionID, 256) {
			return h.callFailed(ctx, claimed, &CallError{Reason: typev1.ReasonCode_REASON_CODE_INTERNAL, Msg: "native: the resumed provider identity is invalid"})
		}
		providerID = receipt.ProviderSessionID
		if _, err := h.db.BindNativeProviderSession(ctx, sessionID, h.machine, session.Generation, claimed.EffectID, providerID); err != nil {
			h.abandon(ctx, claimed)
			return h.resolveFailed(ctx, claimed, err)
		}
	}
	resolved, resErr := h.resolveConfirmed(ctx, claimed, providerID, sessionID)
	if resErr != nil {
		return resErr
	}
	_ = resolved
	h.metrics.resolved(string(claimed.Kind), string(ring.NativeEffectConfirmed))
	logResolvedLine(h.log, string(claimed.Kind), string(ring.NativeEffectConfirmed),
		claimed.CommandID, sessionID, claimed.EffectID, "resume admitted")
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_ResumeNativeSession{ResumeNativeSession: &agentv1.ResumeNativeSessionResult{
			EffectId: claimed.EffectID, AdmissionOrder: claimed.AdmissionOrder,
			ProviderSessionKept: receipt.ProviderSessionKept,
		}},
	}
}

// runInterrupt stops the running turn now, keeping the session. It bypasses
// the ordinary admission order so an interrupt never waits behind the turn
// it stops; qualification, policy and ownership checks still apply.
func (h *Host) runInterrupt(ctx context.Context, cmd *agentv1.Command, digest string) *agentv1.CommandResult {
	body := cmd.GetInterruptNativeSession()
	if body == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: interrupt arm missing")
	}
	sessionID := body.GetSession().GetValue()
	if !validSessionID(sessionID) {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: the session id is invalid")
	}
	session, refusal := h.startSession(ctx, sessionID, body.GetOwnershipGeneration(), true)
	if refusal != nil {
		return refusal
	}
	backend, ok := BackendOf(session.Harness)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED, "native: the session backend is unknown to this agent")
	}
	adapter, ok := h.adapterFor(backend)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED, "native: the backend is not qualified on this machine")
	}
	effect, dup, err := h.admit(ctx, cmd, sessionID, session.Generation, ring.NativeEffectInterrupt, digest)
	if err != nil {
		return h.refuse(ctx, effect, err)
	}
	if dup {
		return h.reenter(ctx, effect, ring.NativeEffectInterrupt)
	}
	claimed, refusal := h.claim(ctx, effect, true)
	if refusal != nil {
		return refusal
	}
	receipt, callErr := adapter.Interrupt(ctx, sessionID, session.Generation)
	if callErr != nil {
		return h.callFailed(ctx, claimed, callErr)
	}
	resolved, resErr := h.resolveConfirmed(ctx, claimed, sessionID, "interrupt")
	if resErr != nil {
		return resErr
	}
	_ = resolved
	h.metrics.resolved(string(claimed.Kind), string(ring.NativeEffectConfirmed))
	logResolvedLine(h.log, string(claimed.Kind), string(ring.NativeEffectConfirmed),
		claimed.CommandID, sessionID, claimed.EffectID, "interrupt admitted")
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_InterruptNativeSession{InterruptNativeSession: &agentv1.InterruptNativeSessionResult{
			Accepted: receipt.Accepted,
		}},
	}
}

// runStop ends the session and releases its machine-side state. Like an
// interrupt it bypasses the ordinary admission order; cleanup finished,
// process gone and turn ended stay separate facts in the result.
func (h *Host) runStop(ctx context.Context, cmd *agentv1.Command, digest string) *agentv1.CommandResult {
	body := cmd.GetStopNativeSession()
	if body == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: stop arm missing")
	}
	sessionID := body.GetSession().GetValue()
	if !validSessionID(sessionID) {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: the session id is invalid")
	}
	session, refusal := h.startSession(ctx, sessionID, body.GetOwnershipGeneration(), true)
	if refusal != nil {
		return refusal
	}
	backend, ok := BackendOf(session.Harness)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED, "native: the session backend is unknown to this agent")
	}
	adapter, ok := h.adapterFor(backend)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED, "native: the backend is not qualified on this machine")
	}
	effect, dup, err := h.admit(ctx, cmd, sessionID, session.Generation, ring.NativeEffectStop, digest)
	if err != nil {
		return h.refuse(ctx, effect, err)
	}
	if dup {
		return h.reenter(ctx, effect, ring.NativeEffectStop)
	}
	claimed, refusal := h.claim(ctx, effect, true)
	if refusal != nil {
		return refusal
	}
	receipt, callErr := adapter.Stop(ctx, StopSpec{
		SessionID: sessionID, ProviderSessionID: session.ProviderSessionID, Generation: session.Generation,
	})
	if callErr != nil {
		return h.callFailed(ctx, claimed, callErr)
	}
	resolved, resErr := h.resolveConfirmed(ctx, claimed, sessionID, "stop")
	if resErr != nil {
		return resErr
	}
	_ = resolved
	h.metrics.resolved(string(claimed.Kind), string(ring.NativeEffectConfirmed))
	logResolvedLine(h.log, string(claimed.Kind), string(ring.NativeEffectConfirmed),
		claimed.CommandID, sessionID, claimed.EffectID, "stop admitted")
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_StopNativeSession{StopNativeSession: &agentv1.StopNativeSessionResult{
			ProcessExited: receipt.ProcessExited, CleanupComplete: receipt.CleanupComplete,
		}},
	}
}

// callFailed maps an adapter call outcome to a result. An uncertain call
// may have acted: the effect stays for recovery and the answer is
// UNKNOWN_OUTCOME, never retried automatically. A proven no-effect resolves
// NO_EFFECT with absence proof and returns the adapter's reason.
func (h *Host) callFailed(ctx context.Context, claimed ring.NativeEffectRecord, callErr *CallError) *agentv1.CommandResult {
	if callErr == nil {
		callErr = &CallError{Uncertain: true, Msg: "native: the adapter returned nothing"}
	}
	if callErr.Uncertain {
		h.metrics.resolved(string(claimed.Kind), "unknown_outcome")
		logResolvedLine(h.log, string(claimed.Kind), "unknown_outcome",
			claimed.CommandID, claimed.SessionID, claimed.EffectID, "uncertain provider outcome")
		return unknownOutcome("native: uncertain whether the provider acted; the effect is held for reconciliation")
	}
	if _, err := h.db.ResolveNativeEffect(ctx, claimed.EffectID, h.machine, claimed.Generation,
		ring.NativeEffectNoEffect, noCallEvidence(claimed.EffectID)); err != nil {
		return h.resolveFailed(ctx, claimed, err)
	}
	h.metrics.resolved(string(claimed.Kind), string(ring.NativeEffectNoEffect))
	logResolvedLine(h.log, string(claimed.Kind), string(ring.NativeEffectNoEffect),
		claimed.CommandID, claimed.SessionID, claimed.EffectID, callErr.Msg)
	r := fail(callErr.Reason, callErr.Msg)
	if r.GetReason() == typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
		r.Reason = typev1.ReasonCode_REASON_CODE_INTERNAL
	}
	r.Retryable = callErr.Retryable
	if callErr.After > 0 {
		r.RetryAfter = durationpb.New(time.Duration(callErr.After) * time.Second)
	}
	return r
}

// resolveConfirmed records provider-receipt proof for a claimed effect.
func (h *Host) resolveConfirmed(ctx context.Context, claimed ring.NativeEffectRecord, summary, scope string) (ring.NativeEffectRecord, *agentv1.CommandResult) {
	resolved, err := h.db.ResolveNativeEffect(ctx, claimed.EffectID, h.machine, claimed.Generation,
		ring.NativeEffectConfirmed, receiptEvidence(claimed.EffectID, summary+":"+scope))
	if err != nil {
		return ring.NativeEffectRecord{}, h.resolveFailed(ctx, claimed, err)
	}
	return resolved, nil
}

// resolveFailed maps a resolve commit failure (owner moved on, or the store
// itself failed) to a result. The effect keeps whatever state committed;
// this answer never claims more than that.
func (h *Host) resolveFailed(ctx context.Context, claimed ring.NativeEffectRecord, err error) *agentv1.CommandResult {
	_ = ctx
	switch {
	case errors.Is(err, ring.ErrNativeOwner):
		return stale(claimed.SessionID)
	case errors.Is(err, ring.ErrNativeConflict):
		current, rerr := h.db.ReadNativeEffect(ctx, claimed.EffectID)
		if rerr == nil {
			return h.reenter(ctx, current, claimed.Kind)
		}
		return unknownOutcome("native: the resolve did not commit; the effect is held for reconciliation")
	default:
		return unknownOutcome("native: the resolve did not commit; the effect is held for reconciliation")
	}
}

// confirmedResult rebuilds a success result for a re-entered duplicate whose
// effect already confirmed: same ids, same order, never a second call.
func (h *Host) confirmedResult(ctx context.Context, effect ring.NativeEffectRecord, kind ring.NativeEffectKind) *agentv1.CommandResult {
	switch kind {
	case ring.NativeEffectStart:
		session, err := h.db.ReadNativeSession(ctx, effect.SessionID)
		if err != nil {
			return unknownOutcome("native: the start confirmed but the session read failed")
		}
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_OK,
			Detail: &agentv1.CommandResult_StartNativeSession{StartNativeSession: &agentv1.StartNativeSessionResult{
				ProviderSessionId: session.ProviderSessionID, Model: session.Model, Effort: session.Effort,
			}},
		}
	case ring.NativeEffectSendTurn:
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_OK,
			Detail: &agentv1.CommandResult_SendNativeTurn{SendNativeTurn: &agentv1.SendNativeTurnResult{
				EffectId: effect.EffectID, AdmissionOrder: effect.AdmissionOrder,
			}},
		}
	case ring.NativeEffectResume:
		kept := true
		if session, err := h.db.ReadNativeSession(ctx, effect.SessionID); err == nil {
			kept = session.StartEffectID != effect.EffectID
		}
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_OK,
			Detail: &agentv1.CommandResult_ResumeNativeSession{ResumeNativeSession: &agentv1.ResumeNativeSessionResult{
				EffectId: effect.EffectID, AdmissionOrder: effect.AdmissionOrder, ProviderSessionKept: kept,
			}},
		}
	case ring.NativeEffectInterrupt:
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_OK,
			Detail: &agentv1.CommandResult_InterruptNativeSession{InterruptNativeSession: &agentv1.InterruptNativeSessionResult{}},
		}
	case ring.NativeEffectStop:
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_OK,
			Detail: &agentv1.CommandResult_StopNativeSession{StopNativeSession: &agentv1.StopNativeSessionResult{}},
		}
	default:
		return unknownOutcome("native: the effect confirmed but its kind is unknown")
	}
}
