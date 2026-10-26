package hooks

import (
	"context"
	"fmt"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	agentexec "github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// Config wires the EnsureGoalHooks handler (row 20 G1). Home and DataDir
// are the developer's own directories the hook scripts and user configs
// live under; Lookup resolves the command's repo short name to a checkout
// top; Ensure installs and verifies (the production default). Nil Lookup
// refuses every command; nil Ensure uses the real install+verify.
type Config struct {
	Home    string
	DataDir string
	Lookup  func(repo *typev1.RepoRef) (string, error)
	Ensure  func(home, dataDir, harness, workdir, expected string) (*typev1.GoalHooksState, error)
}

// NewHandler returns the exec handler for EnsureGoalHooks commands.
func NewHandler(c Config) agentexec.Handler {
	if c.Ensure == nil {
		c.Ensure = Ensure
	}
	return ensureGoalHooksHandler{cfg: c}
}

type ensureGoalHooksHandler struct {
	cfg Config
}

// Kind is the Command.kind arm's message name.
func (h ensureGoalHooksHandler) Kind() string { return "EnsureGoalHooks" }

// Capabilities advertises every harness shape this build installs (02e
// goal.hooks.<harness>); a harness outside this list stays UNSUPPORTED at
// the envelope, before anything is installed.
func (h ensureGoalHooksHandler) Capabilities() []*typev1.Capability {
	var out []*typev1.Capability
	for _, name := range []string{HarnessClaude, HarnessCodex, HarnessMuse, HarnessCursor} {
		out = append(out, &typev1.Capability{Name: "goal.hooks." + name, Version: 1})
	}
	return out
}

// Class re-runs the command from the start: install+verify is declarative
// (02e), and a replayed command_id that found its hooks returns them.
func (h ensureGoalHooksHandler) Class(*agentv1.Command) agentexec.Class { return agentexec.RepeatSafe }

// Run resolves the checkout, installs and verifies the hooks, and reports
// the hook state back. A repo with no checkout is CHECKOUT_MISSING: the
// hub retries elsewhere, the agent never guesses a workdir.
func (h ensureGoalHooksHandler) Run(ctx context.Context, r agentexec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_EnsureGoalHooks)
	if !ok || cmd.EnsureGoalHooks == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"hooks: EnsureGoalHooks handler got another kind")
	}
	g := cmd.EnsureGoalHooks
	if h.cfg.Home == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"hooks: cannot resolve the home folder")
	}
	var workdir string
	if h.cfg.Lookup != nil {
		w, err := h.cfg.Lookup(g.GetWorkdir().GetRepo())
		if err == nil {
			workdir = w
		}
	}
	if workdir == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
			"no local checkout of that repo")
	}
	state, err := h.cfg.Ensure(h.cfg.Home, h.cfg.DataDir, g.GetHarness().GetName(), workdir, g.GetExpectedVersion())
	if err != nil {
		return refuse(typev1.Status_STATUS_FAILED, ReasonOf(err),
			fmt.Sprintf("hooks: install %s: %s", g.GetHarness().GetName(), err))
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK, Reason: typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Message: fmt.Sprintf("hooks: %s installed and verified (version %s)", g.GetHarness().GetName(), state.GetHookVersion()),
		Detail: &agentv1.CommandResult_EnsureGoalHooksResult{
			EnsureGoalHooksResult: &agentv1.EnsureGoalHooksResult{State: state},
		},
	}
}

// Resume re-runs from the start: there are no steps to continue from.
func (h ensureGoalHooksHandler) Resume(ctx context.Context, r agentexec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// refuse builds a failed or refused result: nothing was installed.
func refuse(st typev1.Status, why typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: st, Reason: why, Message: msg}
}
