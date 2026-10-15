package probe

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	agentexec "github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// EnsureProbeHandler runs EnsureProbe commands: declarative, repeat-safe,
// one pass per run_now.
func (m *Manager) EnsureProbeHandler() agentexec.Handler { return ensureProbeHandler{m} }

type ensureProbeHandler struct{ m *Manager }

func (h ensureProbeHandler) Kind() string { return "EnsureProbe" }

// Capabilities advertises the harnesses this build probes. The hub sends
// one EnsureProbe per account with requires naming its harness; a harness
// outside this list (OpenCode takes no probe session) stays UNSUPPORTED
// at the envelope, before anything is typed.
func (h ensureProbeHandler) Capabilities() []*typev1.Capability { return probeCapabilities() }

// probeCapabilities is the one capability list both handlers advertise:
// the harnesses this build probes, each at version 1 (02e).
func probeCapabilities() []*typev1.Capability {
	var out []*typev1.Capability
	for _, name := range []string{"claude", "codex", "muse", "cursor"} {
		out = append(out, &typev1.Capability{Name: "usage.probe." + name, Version: 1})
	}
	return out
}

// Class re-runs the command from the start: EnsureProbe is declarative,
// and a replayed command_id that found its row returns the row's answer.
func (h ensureProbeHandler) Class(*agentv1.Command) agentexec.Class { return agentexec.RepeatSafe }

// Run hands the command to the manager goroutine and waits for its
// answer, within the command's context.
func (h ensureProbeHandler) Run(ctx context.Context, r agentexec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_EnsureProbe)
	if !ok || cmd.EnsureProbe == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: EnsureProbe handler got another kind")
	}
	reply := make(chan *agentv1.CommandResult, 1)
	select {
	case h.m.reqs <- request{ensure: cmd.EnsureProbe, reply: reply}:
	case <-ctx.Done():
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: manager did not take the command")
	}
	select {
	case res := <-reply:
		return res
	case <-ctx.Done():
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: command ended before the pass finished")
	}
}

// Resume re-runs from the start: there are no steps to continue from.
func (h ensureProbeHandler) Resume(ctx context.Context, r agentexec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// StopProbeHandler runs StopProbe commands: declarative and repeat-safe.
func (m *Manager) StopProbeHandler() agentexec.Handler { return stopProbeHandler{m} }

type stopProbeHandler struct{ m *Manager }

func (h stopProbeHandler) Kind() string { return "StopProbe" }

func (h stopProbeHandler) Capabilities() []*typev1.Capability { return probeCapabilities() }

func (h stopProbeHandler) Class(*agentv1.Command) agentexec.Class { return agentexec.RepeatSafe }

func (h stopProbeHandler) Run(ctx context.Context, r agentexec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_StopProbe)
	if !ok || cmd.StopProbe == nil {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: StopProbe handler got another kind")
	}
	reply := make(chan *agentv1.CommandResult, 1)
	select {
	case h.m.reqs <- request{stop: cmd.StopProbe, reply: reply}:
	case <-ctx.Done():
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: manager did not take the command")
	}
	select {
	case res := <-reply:
		return res
	case <-ctx.Done():
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: command ended before the stop finished")
	}
}

func (h stopProbeHandler) Resume(ctx context.Context, r agentexec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
