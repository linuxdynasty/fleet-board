package native

import (
	"context"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// backendName is the journal and capability spelling of a backend: the enum
// value without its NATIVE_BACKEND_ prefix, lowercased ("claude_code").
func backendName(b typev1.NativeBackend) string {
	name := typev1.NativeBackend_name[int32(b)]
	short, ok := strings.CutPrefix(name, "NATIVE_BACKEND_")
	if !ok || short == "" || short == "UNSPECIFIED" {
		return ""
	}
	return strings.ToLower(short)
}

// BackendOf maps a journal harness spelling back to the wire enum, or false
// when this build does not know it: an unknown backend fails closed.
func BackendOf(harness string) (typev1.NativeBackend, bool) {
	for _, b := range []typev1.NativeBackend{
		typev1.NativeBackend_NATIVE_BACKEND_CLAUDE_CODE,
		typev1.NativeBackend_NATIVE_BACKEND_CODEX,
		typev1.NativeBackend_NATIVE_BACKEND_CURSOR,
		typev1.NativeBackend_NATIVE_BACKEND_MUSE,
		typev1.NativeBackend_NATIVE_BACKEND_OPENCODE,
	} {
		if backendName(b) == harness {
			return b, true
		}
	}
	return typev1.NativeBackend_NATIVE_BACKEND_UNSPECIFIED, false
}

// CapabilityFor is the advertised capability that gates one backend:
// "native.<backend>", version 1. The host advertises it only while a
// qualified Adapter for the backend is registered; the executor then
// answers any other native command STATUS_UNSUPPORTED naming it.
func CapabilityFor(b typev1.NativeBackend) *typev1.Capability {
	return &typev1.Capability{Name: "native." + backendName(b), Version: 1}
}

// StartSpec is one qualified start call: the journal session fields the
// adapter needs, none of them a path, prompt or secret.
type StartSpec struct {
	SessionID, AccountRef, Profile  string
	RepoHost, RepoPath, Subpath     string
	Model, Effort, QualificationRef string
	Interface                       string
}

// TurnSpec is one qualified turn call after the start bound its provider id.
type TurnSpec struct {
	SessionID, ProviderSessionID string
	Generation, Turn             uint64
	Input                        string
}

// ResumeSpec continues a session whose process ended or stream broke.
type ResumeSpec struct {
	SessionID, ProviderSessionID string
	Generation                   uint64
}

// StopSpec ends a session and releases its machine-side state.
type StopSpec struct {
	SessionID, ProviderSessionID string
	Generation                   uint64
}

// StartReceipt is what a start call proved: the bound provider identity and
// the effective model and effort. The receipt text is bounded and non-secret;
// the host digests it as the confirm evidence.
type StartReceipt struct {
	ProviderSessionID string
	Model, Effort     string
}

// TurnReceipt proves one turn was handed to the provider exactly once.
type TurnReceipt struct {
	// ProviderTurnID is the provider's identity for the turn, empty when
	// the backend names none. Bounded and non-secret.
	ProviderTurnID string
}

// ResumeReceipt proves the resume call landed, and whether the provider
// session survived it.
type ResumeReceipt struct {
	ProviderSessionKept bool
	// ProviderSessionID is the fresh provider identity when the session was
	// not kept; empty keeps the bound one.
	ProviderSessionID string
}

// InterruptReceipt proves the interrupt call landed.
type InterruptReceipt struct {
	// Accepted is false when no turn was running; the call still happened.
	Accepted bool
}

// StopReceipt proves the stop call landed and what it released.
type StopReceipt struct {
	ProcessExited   bool
	CleanupComplete bool
}

// CallError is a refused or uncertain adapter call. Uncertain means the
// provider may have acted: the host leaves the effect for recovery and
// answers UNKNOWN_OUTCOME, never a blind duplicate or a forged success.
// Refused carries the wire reason for the command result.
type CallError struct {
	Uncertain bool
	Reason    typev1.ReasonCode
	Msg       string
	Retryable bool
	After     int64 // RetryAfter seconds, when Retryable and known
}

func (e *CallError) Error() string { return e.Msg }

// Adapter is one qualified backend's machine-side caller. Exactly one call
// happens per claimed journal effect, outside every store transaction; a
// second call for the same effect never happens. Implementations spawn and
// supervise harness processes only through internal/proc with a measured
// caller, read no credential material, and pass the prelaunch boundary of
// their backend's qualification (#267-#269) before any start.
type Adapter interface {
	// Backend is the wire backend this adapter speaks.
	Backend() typev1.NativeBackend
	// Version is the advertised capability version (1).
	Version() uint32
	// Start opens the provider session and returns its identity.
	Start(ctx context.Context, spec StartSpec) (StartReceipt, *CallError)
	// SendTurn hands one turn to the provider exactly once.
	SendTurn(ctx context.Context, turn TurnSpec) (TurnReceipt, *CallError)
	// Resume continues the session after its process ended or broke.
	Resume(ctx context.Context, rs ResumeSpec) (ResumeReceipt, *CallError)
	// Interrupt stops the running turn now, keeping the session.
	Interrupt(ctx context.Context, sessionID string, generation uint64) (InterruptReceipt, *CallError)
	// Stop ends the session and releases its machine-side state.
	Stop(ctx context.Context, st StopSpec) (StopReceipt, *CallError)
	// Classify translates one provider event into the host's vocabulary.
	// Unknown kinds become ClassUnknown: recorded, never completed.
	Classify(raw RawEvent) Classified
}

// RawEvent is one provider event before classification. Payload is bounded
// by the adapter and never persisted raw; the unknown-event record keeps
// only a digest of it.
type RawEvent struct {
	// Kind is the provider's event name, empty when unreadable.
	Kind string
	// Payload is the bounded raw payload for classification only.
	Payload []byte
	// Stream names which provider stream carried it ("stdout", "stderr",
	// "event"), empty when the adapter does not separate streams.
	Stream string
	// Turn is the provider's turn identity when the event belongs to one,
	// empty otherwise. The host maps it to the journal admission order.
	Turn string
}

// Class names what the host does with a classified provider event.
type Class int

const (
	// ClassUnknown is an event with no known kind: recorded as identity
	// only, never completing anything.
	ClassUnknown Class = iota
	// ClassOutput is harness text for the bounded buffer and SHED sample.
	ClassOutput
	// ClassTool is one tool call started, finished or failed.
	ClassTool
	// ClassApproval is an operator approval request: NEVER_DROP.
	ClassApproval
	// ClassUsage is provider-metered tokens: COALESCE per session.
	ClassUsage
	// ClassTurnEnd finishes exactly one turn: never a goal, exit or cleanup.
	ClassTurnEnd
	// ClassExit is the native process exiting: never a turn end.
	ClassExit
)

// Classified is one provider event in the host's vocabulary. Text fields
// are pre-cap; the host scrubs and caps them before emitting. Unknown
// carries only identity: the host must not complete anything from it.
type Classified struct {
	Class Class
	// Output text (ClassOutput), tool detail (ClassTool), approval prompt
	// (ClassApproval), turn error (ClassTurnEnd FAILED). Never a secret in
	// the adapter's output: the host scrubs again before emitting.
	Text string
	// Tool name (ClassTool, ClassApproval), approval id (ClassApproval),
	// exit signal (ClassExit), raw kind (ClassUnknown).
	Name string
	// Phase for ClassTool ("started", "finished", "failed"); outcome for
	// ClassTurnEnd ("completed", "interrupted", "failed", "uncertain");
	// exit status text for ClassExit ("" when unreadable); approval tool
	// for ClassApproval.
	Detail string
	// Usage tokens for ClassUsage.
	InputTokens, OutputTokens uint64
	// Expected for ClassExit: the stop the hub asked for caused it.
	Expected bool
}
