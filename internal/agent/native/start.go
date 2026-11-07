package native

import (
	"context"
	"errors"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Starting a session is the one path that creates ownership: admit first
// (a duplicate replays without touching the session), create on the
// unknown-session admission, then claim, bind and resolve.

// runStart opens one managed session: validate, record ownership, admit one
// effect, claim it, call the qualified adapter once, bind the provider id
// and resolve CONFIRMED. A bound session refuses a second start; an
// unbound one with an in-flight start serializes on the journal order and
// the loser re-checks the binding after its claim.
func (h *Host) runStart(ctx context.Context, cmd *agentv1.Command, digest string) *agentv1.CommandResult {
	body := cmd.GetStartNativeSession()
	if body == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: start arm missing")
	}
	backend := body.GetBackend()
	if backendName(backend) == "" {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED, "native: the backend is unknown to this agent")
	}
	adapter, ok := h.adapterFor(backend)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED, "native: the backend is not qualified on this machine")
	}
	sessionID := body.GetSession().GetValue()
	if !validSessionID(sessionID) {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: the session id is invalid")
	}
	iface := body.GetInterface()
	if iface == "" {
		iface = "default"
	}
	// Empty profile, model and effort mean the harness default; every other
	// reference must be present and bounded.
	if !validRef(iface, 64) || !validRef(body.GetAccount().GetValue(), 32) ||
		!validRef(body.GetQualificationRef(), 128) ||
		!validSubpath(body.GetWorkdirSubpath()) ||
		!validRef(body.GetRepo().GetHost(), 128) || !validRef(body.GetRepo().GetPath(), 256) ||
		(body.GetAccountProfile() != "" && !validRef(body.GetAccountProfile(), 128)) ||
		(body.GetModel() != "" && !validRef(body.GetModel(), 128)) ||
		(body.GetEffort() != "" && !validRef(body.GetEffort(), 64)) {
		return fail(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "native: a start reference is invalid")
	}
	harness := backendName(backend)
	// Admit before creating: a duplicate command_id replays the stored
	// effect without touching the session, so the first outcome stands.
	effect, dup, err := h.admit(ctx, cmd, sessionID, 1, ring.NativeEffectStart, digest)
	if err != nil && !errors.Is(err, ring.ErrNativeUnknownSession) {
		return h.refuse(ctx, effect, err)
	}
	if errors.Is(err, ring.ErrNativeUnknownSession) {
		stored, cerr := h.createNativeSession(ctx, sessionID, harness, iface, body)
		if cerr != nil {
			return cerr
		}
		if stored.ProviderSessionID != "" || stored.StartEffectID != "" {
			return fail(typev1.ReasonCode_REASON_CODE_NATIVE_ALREADY_MANAGED, "native: the session is already managed")
		}
		effect, dup, err = h.admit(ctx, cmd, sessionID, 1, ring.NativeEffectStart, digest)
		if err != nil {
			return h.refuse(ctx, effect, err)
		}
	}
	if dup {
		return h.reenter(ctx, effect, ring.NativeEffectStart)
	}
	claimed, refusal := h.claim(ctx, effect, false)
	if refusal != nil {
		return refusal
	}
	// The order wait serialized concurrent starts: re-check the binding
	// after the claim, before the one external call.
	bound, err := h.db.ReadNativeSession(ctx, sessionID)
	if err != nil {
		h.abandon(ctx, claimed)
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the session read failed")
	}
	if bound.ProviderSessionID != "" {
		h.abandon(ctx, claimed)
		return fail(typev1.ReasonCode_REASON_CODE_NATIVE_ALREADY_MANAGED, "native: the session is already managed")
	}
	receipt, callErr := adapter.Start(ctx, StartSpec{
		SessionID: sessionID, AccountRef: bound.AccountRef, Profile: bound.ProfileRef,
		RepoHost: body.GetRepo().GetHost(), RepoPath: body.GetRepo().GetPath(),
		Subpath: body.GetWorkdirSubpath(), Model: bound.Model, Effort: bound.Effort,
		QualificationRef: body.GetQualificationRef(), Interface: bound.Interface,
	})
	if callErr != nil {
		return h.callFailed(ctx, claimed, callErr)
	}
	if !validRef(receipt.ProviderSessionID, 256) {
		return h.callFailed(ctx, claimed, &CallError{Reason: typev1.ReasonCode_REASON_CODE_INTERNAL, Msg: "native: the provider identity is invalid"})
	}
	// Bind before resolving, like resume: a crash between the two commits
	// leaves a bound-but-unconfirmed session (recovered without a second
	// provider call), never a confirmed session with no provider id.
	if _, err := h.db.BindNativeProviderSession(ctx, sessionID, h.machine, 1, claimed.EffectID, receipt.ProviderSessionID); err != nil {
		h.abandon(ctx, claimed)
		return h.resolveFailed(ctx, claimed, err)
	}
	resolved, resErr := h.resolveConfirmed(ctx, claimed, receipt.ProviderSessionID, receipt.Model+"/"+receipt.Effort)
	if resErr != nil {
		return resErr
	}
	_ = resolved
	h.metrics.resolved(string(claimed.Kind), string(ring.NativeEffectConfirmed))
	logResolvedLine(h.log, string(claimed.Kind), string(ring.NativeEffectConfirmed),
		claimed.CommandID, sessionID, claimed.EffectID, "start bound")
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_StartNativeSession{StartNativeSession: &agentv1.StartNativeSessionResult{
			ProviderSessionId: receipt.ProviderSessionID, Model: receipt.Model, Effort: receipt.Effort,
		}},
	}
}

// createNativeSession records the initial managed controller for a start
// whose admission found no session row. A conflicting row (same id, other
// owner) means the session is already managed elsewhere.
func (h *Host) createNativeSession(ctx context.Context, sessionID, harness, iface string, body *agentv1.StartNativeSession) (ring.NativeSessionRecord, *agentv1.CommandResult) {
	// The journal stores no empty references: an empty profile, model or
	// effort (the harness default) is recorded as "default".
	profile, model, effort := body.GetAccountProfile(), body.GetModel(), body.GetEffort()
	if profile == "" {
		profile = "default"
	}
	if model == "" {
		model = "default"
	}
	if effort == "" {
		effort = "default"
	}
	st, err := h.db.State(ctx)
	if err != nil {
		return ring.NativeSessionRecord{}, fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the install binding is unreadable")
	}
	stored, err := h.db.CreateNativeSession(ctx, ring.NativeSessionRecord{
		SessionID: sessionID, MachineID: h.machine, InstallID: st.InstallID,
		Harness: harness, Interface: iface,
		AccountRef: body.GetAccount().GetValue(), ProfileRef: profile,
		WorkdirRef:       workRef(body.GetRepo().GetHost(), body.GetRepo().GetPath(), body.GetWorkdirSubpath()),
		Model:            model,
		Effort:           effort,
		QualificationRef: body.GetQualificationRef(),
		ControllerID:     h.machine, Generation: 1, ControlState: ring.NativeControlManaged,
	})
	if err != nil {
		if errors.Is(err, ring.ErrNativeConflict) || errors.Is(err, ring.ErrNativeOwner) {
			return ring.NativeSessionRecord{}, fail(typev1.ReasonCode_REASON_CODE_NATIVE_ALREADY_MANAGED, "native: the session is already managed")
		}
		return ring.NativeSessionRecord{}, fail(typev1.ReasonCode_REASON_CODE_INTERNAL, "native: the session record did not commit")
	}
	return stored, nil
}
