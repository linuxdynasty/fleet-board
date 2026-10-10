package conn

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const (
	// maxCapabilityName caps a capability name in Hello.
	maxCapabilityName = 64
	// reservedCapability is never advertised (02e).
	reservedCapability = "exec.raw"
)

// intake hands a Command or Settings to its hook with this stream's epoch
// and a context that ends with the stream (ruling fork 12 c). A hook must
// not block, nor wait on Loop.Send: it runs on the stream's sending
// goroutine. A kind with no hook is logged and ignored. A hook
// that refuses (its intake is full) drops the message unlogged and
// unacked, so the hub's re-send covers it; the drop is logged and counted.
func (s *session) intake(ctx context.Context, m *agentv1.ConnectResponse) {
	l := s.l
	var kind string
	var taken, hooked bool
	args := []any{}
	switch b := m.GetBody().(type) {
	case *agentv1.ConnectResponse_Command:
		kind = "command"
		if hook := l.opts.Command; hook != nil {
			stop := s.watchHook("command")
			hooked, taken = true, hook(s.hctx, s.epoch, b.Command)
			stop()
		}
		if id := b.Command.GetCommandId().GetValue(); printable(id, maxIDBytes) {
			args = append(args, "command_id", id)
		}
	case *agentv1.ConnectResponse_Settings:
		kind = "settings"
		if hook := l.opts.Settings; hook != nil {
			stop := s.watchHook("settings")
			hooked, taken = true, hook(s.hctx, s.epoch, b.Settings)
			stop()
		}
	default:
		return
	}
	switch {
	case !hooked:
		l.log.InfoContext(ctx, "connect.message_ignored", "kind", kind)
	case !taken:
		l.log.WarnContext(ctx, "command.intake_full", append([]any{"kind", kind}, args...)...)
		l.m.intakeDrop(ctx, kind)
	}
}

// hookHeldWarn is how long a hook may hold the sending goroutine before
// connect.hook_blocked is logged: nothing is sent, not even a heartbeat,
// while it runs.
const hookHeldWarn = 10 * time.Second

// watchHook logs connect.hook_blocked{hook, held_ms} once if the hook
// named runs longer than hookHeldWarn; call the returned func when it
// returns.
func (s *session) watchHook(hook string) func() {
	d := hookHeldWarn
	if s.l.opts.hookWarn > 0 {
		d = s.l.opts.hookWarn
	}
	t := time.AfterFunc(d, func() {
		s.l.log.WarnContext(s.hctx, "connect.hook_blocked", "hook", hook, "held_ms", d.Milliseconds())
	})
	return func() { t.Stop() }
}

// capabilities is what Hello advertises: the hook's list without the
// reserved exec.raw, an invalid entry (no name, a name outside
// [a-z0-9._-] or over 64 bytes, version 0) or a repeated name, each
// dropped with connect.capability_dropped{reason, name}, logged once per
// distinct entry and run. Only Run's goroutine calls it.
func (l *Loop) capabilities(ctx context.Context) []*typev1.Capability {
	if l.opts.Capabilities == nil {
		return nil
	}
	var out []*typev1.Capability
	seen := map[string]bool{}
	for _, c := range l.opts.Capabilities() {
		name, reason := c.GetName(), ""
		switch {
		case !capabilityName(name) || c.GetVersion() == 0:
			reason = "invalid"
		case name == reservedCapability:
			reason = "reserved"
		case seen[name]:
			reason = "repeat"
		}
		if reason != "" {
			key := fmt.Sprintf("%s %q %d", reason, name, c.GetVersion())
			if !l.capsDropped[key] {
				if l.capsDropped == nil {
					l.capsDropped = map[string]bool{}
				}
				l.capsDropped[key] = true
				args := []any{"reason", reason}
				if capabilityName(name) {
					args = append(args, "name", name)
				}
				l.log.WarnContext(ctx, "connect.capability_dropped", args...)
			}
			continue
		}
		seen[name] = true
		out = append(out, &typev1.Capability{Name: name, Version: c.GetVersion()})
	}
	return out
}

func capabilityName(s string) bool {
	if s == "" || len(s) > maxCapabilityName {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
