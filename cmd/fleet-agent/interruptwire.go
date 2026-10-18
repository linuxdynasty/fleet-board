package main

import (
	"context"
	"path/filepath"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/control"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const interruptCapability = "tmux.default_server.interrupt"

// interruptOnly advertises and executes only F4 on the developer's default
// server. The merged settings are read at the last possible point, after a
// possible local approval wait and again on restart recovery.
type interruptOnly struct {
	inner    exec.Handler
	settings *settings.Manager
}

func (h interruptOnly) Kind() string { return "Interrupt" }

func (h interruptOnly) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: interruptCapability, Version: 1}}
}

func (h interruptOnly) Class(c *agentv1.Command) exec.Class { return h.inner.Class(c) }

func (h interruptOnly) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	// The scheduler may touch only fleet-owned sessions. This slice cannot
	// prove ownership at the agent, so it refuses unattended Interrupt.
	if r.Command.GetRequestedBy().GetRole() == typev1.Role_ROLE_SCHEDULER {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_REJECTED_BY_POLICY, Reason: typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT,
			Message: "the scheduler cannot interrupt a pane without local fleet-ownership proof",
		}
	}
	if h.settings.SafeMode() {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_REJECTED_BY_POLICY, Reason: typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE,
			Message: "the local policy is in safe mode; no pane can be interrupted",
		}
	}
	effective := h.settings.Effective()
	if effective.GetValues().GetDefaultServer() != typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_REJECTED_BY_POLICY, Reason: typev1.ReasonCode_REASON_CODE_POLICY_DENIED,
			Message: "the effective default-server policy does not allow control",
		}
	}
	return h.inner.Run(ctx, r)
}

func (h interruptOnly) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// interruptHandler binds the existing F4 implementation to the observer's
// live default client. No other control handler is registered by this run.
func interruptHandler(tmx tmuxRun, sm *settings.Manager, tel *telemetry.Telemetry) (exec.Handler, error) {
	if tmx.obs == nil || tmx.obs.Client("default") == nil {
		return nil, nil
	}
	runner, err := proc.NewRunner(tel)
	if err != nil {
		return nil, err
	}
	caller, err := proc.For("tmux")
	if err != nil {
		return nil, err
	}
	e, err := control.NewInterrupt(control.Options{
		Runner: runner, Caller: caller, Tmux: tmx.program,
		Socket: filepath.Join(tmx.obs.Folder(), "default"), Server: "default",
		Kind:   typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
		Sender: tmx.obs.Client("default"),
	})
	if err != nil {
		return nil, err
	}
	return interruptOnly{inner: e.InterruptHandler(), settings: sm}, nil
}
