package exec

import (
	"context"
	"fmt"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Class is what the executor does with a command that was running when the
// agent stopped (ruling fork 4 iii; 02e "Repeat safe"; 02h step table).
type Class int

// The restart classes. The zero value is NotRepeatSafe, so a handler that
// says nothing fails closed.
const (
	// NotRepeatSafe ends a running command UNKNOWN_OUTCOME after a restart
	// (SendKeys, a non-OBSERVE RunHarnessCommand; 02-contracts).
	NotRepeatSafe Class = iota
	// RepeatSafe runs the command again from the start.
	RepeatSafe
	// PerStep hands the command to the handler's Resume with the step the
	// log reached (StartGoal, 02h).
	PerStep
)

func (c Class) String() string {
	switch c {
	case RepeatSafe:
		return "repeat_safe"
	case PerStep:
		return "per_step"
	default:
		return "not_repeat_safe"
	}
}

// Request is one command handed to its handler.
type Request struct {
	Command *agentv1.Command
	// Step is the step the command log reached; Resume reads it.
	Step int
	// FleetOnly says the role cell allows the command only in a session or
	// window the fleet started (02g `F`); FleetOrMarked also allows a pane
	// the developer marked available with fresh_context_on_reuse (`F*`).
	// The handler checks them: only it can see tmux.
	FleetOnly, FleetOrMarked bool
	// Progress reports a step of a composite. The executor marks the step
	// in the command log and sends it; a nil return is not "sent", only
	// "recorded and queued or kept for the next stream".
	Progress func(ctx context.Context, p *agentv1.CommandProgress) error
}

// Handler runs one command kind. Every handler is a later plan row; row 6
// registers none, so every command is UNSUPPORTED.
type Handler interface {
	// Kind is the Command.kind arm's message name, such as "CapturePane".
	Kind() string
	// Capabilities are advertised in Hello while the handler is registered.
	Capabilities() []*typev1.Capability
	// Class is what to do with the command after a restart.
	Class(c *agentv1.Command) Class
	// Run does the command and returns its result: status, reason,
	// message, metadata and detail. The executor sets the id, the times
	// and the trace, and scrubs the message.
	Run(ctx context.Context, r Request) *agentv1.CommandResult
	// Resume continues a PerStep command from r.Step after a restart.
	Resume(ctx context.Context, r Request) *agentv1.CommandResult
}

// Variant is implemented by a handler whose role-matrix row depends on
// state the message does not carry: KillWindow on a fleet window
// ("fleet_window") or a developer's ("developer_window", the default).
type Variant interface {
	Variant(c *agentv1.Command) string
}

// registry is the handlers by kind.
type registry map[string]Handler

func newRegistry(hs []Handler) (registry, error) {
	r := registry{}
	for _, h := range hs {
		k := h.Kind()
		if !isKind(k) {
			return nil, fmt.Errorf("exec: handler for unknown kind %q", k)
		}
		if r[k] != nil {
			return nil, fmt.Errorf("exec: two handlers for %s", k)
		}
		r[k] = h
	}
	return r, nil
}

// capabilities are what every registered handler advertises.
func (r registry) capabilities() []*typev1.Capability {
	var out []*typev1.Capability
	for _, h := range r {
		out = append(out, h.Capabilities()...)
	}
	return out
}

// supports reports whether a handler for kind is registered and some
// handler advertises the capability the command requires, at its version
// or above. A command with no requires is never supported.
func (r registry) supports(kind string, req *typev1.Capability) bool {
	if r[kind] == nil || req.GetName() == "" {
		return false
	}
	for _, c := range r.capabilities() {
		if c.GetName() == req.GetName() && c.GetVersion() >= req.GetVersion() {
			return true
		}
	}
	return false
}

// Handlers is the production registry. It is empty in plan row 6: each
// kind's handler, and the capability it advertises, comes with the row that
// builds it (03n "Capabilities"), so until then every command is
// UNSUPPORTED.
func Handlers() []Handler { return nil }
