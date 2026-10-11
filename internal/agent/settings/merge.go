package settings

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// Input is what [Merge] merges.
type Input struct {
	// Hub is the Settings message (on a restart, the one kept in agent.db).
	Hub *agentv1.Settings
	// Prev is the Settings kept before it (nil when none): an invalid field
	// keeps its value from there.
	Prev *agentv1.Settings
	// Local is the policy file in force (the ceiling); never nil.
	Local *policy.Policy
	// SafeMode is policy safe mode: every hub setting is rejected.
	SafeMode bool
}

// Capped is one field the local ceiling lowered. Hub and Local are the two
// values for a number, a duration or a mode, and empty for a list (a list
// may hold paths, which are never logged).
type Capped struct{ Field, Hub, Local string }

// Outcome is what [Merge] gives.
type Outcome struct {
	// Kept is the hub's Settings as the agent keeps it in agent.db.
	Kept *agentv1.Settings
	// Ack is the SettingsAck for the message, with the effective values.
	Ack    *agentv1.SettingsAck
	Capped []Capped
}

// Merge is the tighten-only merge of 03e, field by field, of the hub's
// settings under the local policy file: the file can only make a hub value
// stricter, never looser. A field no layer states takes the built-in
// default. It is a pure function of its input.
func Merge(in Input) Outcome {
	kept, rej := keep(in.Hub, in.Prev)
	h, labels, probes := kept.GetValues(), kept.GetLabels(), kept.GetProbeIntervalByAccount()
	if in.SafeMode {
		rej = rej[:0]
		for _, f := range sent(in.Hub) {
			rej = append(rej, rejection{field: f, reason: typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE, msg: "the policy file is invalid: the agent is in safe mode"})
		}
		h, labels, probes = &typev1.SettingValues{}, nil, nil
	}
	l := in.Local
	m := &merger{eff: &typev1.EffectiveSettings{Generation: in.Hub.GetGeneration(), Values: &typev1.SettingValues{}}}
	v := m.eff.GetValues()
	minNum := func(a, b uint32) bool { return a < b }
	tighten(m, fMaxActive, h.MaxActive, l.MaxActive, ptr[uint32](defMaxActive), minNum, func(x uint32) { v.MaxActive = &x }, num)
	tighten(m, fMaxTotal, h.MaxTotal, l.MaxTotal, ptr[uint32](defMaxTotal), minNum, func(x uint32) { v.MaxTotal = &x }, num)
	tighten(m, fMaxWindows, h.MaxWindows, l.MaxWindows, ptr[uint32](defMaxWindows), minNum, func(x uint32) { v.MaxWindows = &x }, num)
	m.approvals(h.GetLocalApproval(), l.LocalApproval)
	m.rates(h.GetRateLimits(), l.RateLimits)
	shorter := func(a, b time.Duration) bool { return a < b }
	tighten(m, fCertLifetime, dur(h.GetCertLifetime()), l.CertLifetime, ptr(defCertLifetime), shorter, func(d time.Duration) { v.CertLifetime = durationpb.New(d) }, time.Duration.String)
	m.harnesses(h.GetAllowedHarnesses(), l.AllowedHarnesses)
	m.roots(h.GetAllowedRepoRoots(), l.AllowedRepoRoots)
	tighten(m, fDefaultServer, h.DefaultServer, mode(l.DefaultServer), ptr(defDefaultServer),
		func(a, b typev1.DefaultServerMode) bool { return a < b },
		func(x typev1.DefaultServerMode) { v.DefaultServer = x.Enum() }, typev1.DefaultServerMode.String)
	hubWins(m, fWindowNameTemplate, h.WindowNameTemplate, l.WindowNameTemplate, ptr(defWindowNameTemplate), func(s string) { v.WindowNameTemplate = &s })
	hubWins(m, fIdleThreshold, dur(h.GetIdleThreshold()), l.IdleThreshold, ptr(defIdleThreshold), func(d time.Duration) { v.IdleThreshold = durationpb.New(d) })
	tighten(m, fAttachIdleTimeout, dur(h.GetAttachIdleTimeout()), l.AttachIdleTimeout, ptr(defAttachIdleTimeout), shorter, func(d time.Duration) { v.AttachIdleTimeout = durationpb.New(d) }, time.Duration.String)
	hubWins(m, fShareIdentityEmail, h.ShareIdentityEmail, l.ShareIdentityEmail, ptr(false), func(b bool) { v.ShareIdentityEmail = &b })
	tighten(m, fSpoolCapBytes, h.SpoolCapBytes, l.SpoolCapBytes, ptr[uint64](defSpoolCapBytes), func(a, b uint64) bool { return a < b }, func(x uint64) { v.SpoolCapBytes = &x }, num)
	tighten(m, fBlockOnBattery, h.BlockOnBattery, l.BlockOnBattery, ptr(false), func(a, b bool) bool { return a && !b }, func(b bool) { v.BlockOnBattery = &b }, num[bool])
	m.probes(h.GetProbeInterval(), probes, l.MinProbeInterval)
	if g := h.GetMergedGrace(); g != nil { // the hub's own: echoed unchanged (03e)
		v.MergedGrace = g
		m.source(fMergedGrace, hubLayer)
	}
	m.labels(labels, l.Labels)
	ack := &agentv1.SettingsAck{
		Generation: in.Hub.GetGeneration(), SettingsEpoch: in.Hub.GetSettingsEpoch(), Effective: m.eff,
		CappedFields:               m.cappedFields(),
		UnrecognizedValueFields:    unknownFields(in.Hub.GetValues()),
		UnrecognizedSettingsFields: unknownFields(in.Hub),
	}
	for _, r := range rej {
		ack.Rejected = append(ack.Rejected, &typev1.SettingRejection{Field: r.field, Reason: r.reason, Message: r.msg})
	}
	return Outcome{Kept: kept, Ack: ack, Capped: m.capped}
}

// merger builds the effective settings one field at a time.
type merger struct {
	eff    *typev1.EffectiveSettings
	capped []Capped
}

func (m *merger) source(field string, l typev1.SettingLayer) {
	if l != noLayer {
		m.eff.Sources = append(m.eff.Sources, &typev1.SettingSource{Field: field, Layer: l})
	}
}

func (m *merger) cap(field, hub, local string) {
	m.capped = append(m.capped, Capped{Field: field, Hub: hub, Local: local})
}

func (m *merger) cappedFields() []string {
	var out []string
	for _, c := range m.capped {
		if len(out) == 0 || out[len(out)-1] != c.Field {
			out = append(out, c.Field)
		}
	}
	return out
}

// tighten is a safety field: with both layers set, the local value wins
// only when stricter(local, hub), and the field is then capped.
func tighten[T any](m *merger, field string, hub, local, def *T, stricter func(a, b T) bool, set func(T), show func(T) string) {
	switch {
	case hub != nil && local != nil && stricter(*local, *hub):
		set(*local)
		m.source(field, localLayer)
		m.cap(field, show(*hub), show(*local))
	case hub != nil:
		set(*hub)
		m.source(field, hubLayer)
	case local != nil:
		set(*local)
		m.source(field, localLayer)
	case def != nil:
		set(*def)
		m.source(field, defaultLayer)
	}
}

// hubWins is a field that is not a safety limit: the hub's value stands
// over the file's (03e).
func hubWins[T any](m *merger, field string, hub, local, def *T, set func(T)) {
	tighten(m, field, hub, local, def, func(T, T) bool { return false }, set, func(T) string { return "" })
}

func ptr[T any](v T) *T { return &v }

func num[T any](v T) string { return fmt.Sprint(v) }

func dur(d *durationpb.Duration) *time.Duration {
	if d == nil {
		return nil
	}
	x := d.AsDuration()
	return &x
}

func mode(s *policy.ServerMode) *typev1.DefaultServerMode {
	if s == nil {
		return nil
	}
	switch *s {
	case policy.ServerNone:
		return typev1.DefaultServerMode_DEFAULT_SERVER_MODE_NONE.Enum()
	case policy.ServerObserve:
		return typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE.Enum()
	default:
		return typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL.Enum()
	}
}
