package ring

import (
	"errors"
	"fmt"
	"math"
)

// Native journal errors describe refused storage transitions. The dormant
// journal grants no permission to launch a harness by itself.
var (
	ErrNativeBadInput       = errors.New("ring: invalid native journal input")
	ErrNativeConflict       = errors.New("ring: native identity conflicts")
	ErrNativeUnknownSession = errors.New("ring: unknown native session")
	ErrNativeUnknownEffect  = errors.New("ring: unknown native effect")
	ErrNativeOwner          = errors.New("ring: native controller or generation changed")
	ErrNativeClaimed        = errors.New("ring: native effect already claimed")
	ErrNativeOrder          = errors.New("ring: earlier native effect is unresolved")
	ErrNativeHeld           = errors.New("ring: native session is held for reconciliation")
)

type NativeControlState string

const (
	NativeControlManaged NativeControlState = "managed"
	NativeControlHeld    NativeControlState = "held"
)

// NativeSessionRecord is execution ownership in agent.db. All references are
// non-secret identifiers; WorkdirRef is a logical reference, not a path.
type NativeSessionRecord struct {
	SessionID, MachineID, InstallID                string
	Harness, Interface, AccountRef, ProfileRef     string
	WorkdirRef, Model, Effort, QualificationRef    string
	ControllerID, ProviderSessionID, StartEffectID string
	Generation                                     uint64
	ControlState                                   NativeControlState
}

type NativeEffectKind string

const (
	NativeEffectStart     NativeEffectKind = "start"
	NativeEffectSendTurn  NativeEffectKind = "send_turn"
	NativeEffectResume    NativeEffectKind = "resume"
	NativeEffectInterrupt NativeEffectKind = "interrupt"
	NativeEffectStop      NativeEffectKind = "stop"
)

type NativeEffectState string

const (
	NativeEffectPrepared          NativeEffectState = "prepared"
	NativeEffectMayHaveRun        NativeEffectState = "may_have_run"
	NativeEffectConfirmed         NativeEffectState = "confirmed"
	NativeEffectNoEffect          NativeEffectState = "no_effect"
	NativeEffectReconcileRequired NativeEffectState = "reconcile_required"
)

// NativeEffectRecord is one external intent. CommandBodyDigest is SHA-256 of the
// encoded logged command; no command body, prompt or output enters this table.
type NativeEffectRecord struct {
	EffectID, CommandID, SessionID string
	Generation                     uint64
	Kind                           NativeEffectKind
	CommandBodyDigest              string
	AdmissionOrder                 uint64
	State                          NativeEffectState
	RetryOfEffectID                string
	EvidenceKind, EvidenceDigest   string
}

func validNativeRef(s string, max int) bool {
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

func validNativeID(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' {
					return false
				}
			}
		}
	}
	return true
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}

func (r NativeSessionRecord) validate() error {
	if !validNativeID(r.SessionID, 32) || !validNativeID(r.MachineID, 64) ||
		!validNativeRef(r.InstallID, 64) || !validNativeRef(r.Harness, 64) ||
		!validNativeRef(r.Interface, 64) || !validNativeRef(r.AccountRef, 128) ||
		!validNativeRef(r.ProfileRef, 128) || !validNativeID(r.WorkdirRef, 128) ||
		!validNativeRef(r.Model, 128) || !validNativeRef(r.Effort, 64) ||
		!validNativeRef(r.QualificationRef, 128) || !validNativeRef(r.ControllerID, 128) ||
		r.ProviderSessionID != "" || r.StartEffectID != "" ||
		r.Generation != 1 || r.ControlState != NativeControlManaged {
		return ErrNativeBadInput
	}
	return nil
}

func (r NativeEffectRecord) validate() error {
	if !validNativeRef(r.EffectID, 128) || !validNativeRef(r.CommandID, 128) ||
		!validNativeID(r.SessionID, 32) || r.Generation == 0 || r.Generation > math.MaxInt64 ||
		!validDigest(r.CommandBodyDigest) ||
		(r.RetryOfEffectID != "" && !validNativeRef(r.RetryOfEffectID, 128)) ||
		r.AdmissionOrder != 0 || r.State != "" || r.EvidenceKind != "" || r.EvidenceDigest != "" {
		return ErrNativeBadInput
	}
	switch r.Kind {
	case NativeEffectStart, NativeEffectSendTurn, NativeEffectResume, NativeEffectInterrupt, NativeEffectStop:
		return nil
	default:
		return fmt.Errorf("%w: effect kind", ErrNativeBadInput)
	}
}

func nativePageLimit(limit int) error {
	if limit < 1 || limit > 1000 {
		return fmt.Errorf("%w: page limit must be 1..1000", ErrNativeBadInput)
	}
	return nil
}
