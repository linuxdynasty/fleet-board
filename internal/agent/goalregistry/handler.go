package goalregistry

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// PaneInfo resolves a pane to the checkout it works in and the harness
// running there (the observer inventory plus the checkout table). The
// hub sends no path with RegisterGoal (02h "No path from the hub"), so
// the RegisterGoal handler derives it here. Production wiring is row
// 21's (it already resolves panes for pick_session); until then a nil
// PaneInfo refuses the command instead of guessing.
type PaneInfo func(pane *typev1.PaneKey) (worktree, harness string, err error)

// Services bundles the registry with its command handlers and the
// snapshot fill. files removes goal files on ReleaseGoal; info derives
// checkouts for RegisterGoal.
type Services struct {
	reg   *Registry
	files goalfile.Writer
	info  PaneInfo
}

// NewServices builds the registry side. emit feeds GoalRegistryChanged,
// PaneHarnessSession and HarnessHookEvent into the ring (all NEVER_DROP;
// the existing events_total{kind} counter covers them, so this change
// adds no metric).
func NewServices(emit Emit, now func() time.Time, files goalfile.Writer, info PaneInfo) *Services {
	return &Services{reg: New(emit, now), files: files, info: info}
}

// Registry is the bound registry (snapshot fill, hook calls, Bound).
func (s *Services) Registry() *Registry { return s.reg }

// FillSnapshot carries the whole registry (02h level-triggered).
func (s *Services) FillSnapshot(snap *agentv1.Snapshot) { s.reg.FillSnapshot(snap) }

// IsBound reports whether a pane key (as Tracker.Config.Bound reads it)
// names a registry-bound pane: such a pane never gains an observed goal.
func (s *Services) IsBound(key string) bool {
	for _, entry := range s.reg.Entries() {
		if entry.GetPane().String() == key {
			return true
		}
	}
	return false
}

// RegisterHandler is the RegisterGoal command handler.
func (s *Services) RegisterHandler() exec.Handler { return registerHandler{s} }

// ReleaseHandler is the ReleaseGoal command handler.
func (s *Services) ReleaseHandler() exec.Handler { return releaseHandler{s} }

// Handlers returns both handlers for the executor (capability
// goal.registry v1, 02e).
func (s *Services) Handlers() []exec.Handler {
	return []exec.Handler{s.RegisterHandler(), s.ReleaseHandler()}
}

type registerHandler struct{ s *Services }

func (h registerHandler) Kind() string { return "RegisterGoal" }

func (h registerHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "goal.registry", Version: 1}}
}

// Class re-runs from the start: RegisterGoal is declarative (02h).
func (h registerHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

func (h registerHandler) Run(_ context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_RegisterGoal)
	if !ok || cmd.RegisterGoal == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "goalregistry: RegisterGoal handler got another kind")
	}
	in := cmd.RegisterGoal
	if in.GetPane() == nil || in.GetPane().GetPaneId() == "" {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goalregistry: RegisterGoal needs a pane")
	}
	if !goalfile.ValidGoalID(in.GetGoal().GetValue()) {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goalregistry: RegisterGoal needs a goal id")
	}
	if h.s.info == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "goalregistry: pane checkout lookup is unwired")
	}
	worktree, harness, err := h.s.info(in.GetPane())
	if err != nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "goalregistry: the pane checkout is unknown")
	}
	replaced, err := h.s.reg.Register(in.GetPane(), in.GetGoal().GetValue(), harness, worktree, in.GetRepoDriver())
	if err != nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "goalregistry: RegisterGoal refused")
	}
	return &agentv1.CommandResult{
		Status:   typev1.Status_STATUS_OK,
		Metadata: map[string]string{"goal": in.GetGoal().GetValue(), "replaced": fmt.Sprint(replaced)},
	}
}

// Resume re-runs from the start: both commands are declarative.
func (h registerHandler) Resume(_ context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(context.Background(), r)
}

type releaseHandler struct{ s *Services }

func (h releaseHandler) Kind() string { return "ReleaseGoal" }

func (h releaseHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "goal.registry", Version: 1}}
}

// Class re-runs from the start: ReleaseGoal is declarative (02h).
func (h releaseHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

func (h releaseHandler) Run(_ context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_ReleaseGoal)
	if !ok || cmd.ReleaseGoal == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "goalregistry: ReleaseGoal handler got another kind")
	}
	in := cmd.ReleaseGoal
	if in.GetPane() == nil || in.GetPane().GetPaneId() == "" {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goalregistry: ReleaseGoal needs a pane")
	}
	if !goalfile.ValidGoalID(in.GetGoal().GetValue()) {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goalregistry: ReleaseGoal needs a goal id")
	}
	entry, ok := h.s.reg.ByPane(in.GetPane())
	if !ok || entry.GetGoal().GetValue() != in.GetGoal().GetValue() {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_OK, Metadata: map[string]string{"goal": in.GetGoal().GetValue(), "removed": "false"},
		}
	}
	removed, err := h.s.reg.Release(in.GetPane(), in.GetGoal().GetValue())
	if err != nil || !removed {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "goalregistry: ReleaseGoal did not release")
	}
	if err := h.s.files.ReleaseGoalFile(entry.GetWorktreePath(), in.GetGoal().GetValue()); err != nil {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_FAILED, Reason: typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message:  "goalregistry: the entry released but the goal file stayed",
			Metadata: map[string]string{"goal": in.GetGoal().GetValue(), "released": "true"},
		}
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK, Metadata: map[string]string{"goal": in.GetGoal().GetValue(), "removed": "true"},
	}
}

// Resume re-runs from the start: both commands are declarative.
func (h releaseHandler) Resume(_ context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(context.Background(), r)
}

// failed answers FAILED with a path-free message (the executor scrubs
// the message; metadata carries ids only, never paths or file text).
func failed(reason typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_FAILED, Reason: reason, Message: msg}
}
