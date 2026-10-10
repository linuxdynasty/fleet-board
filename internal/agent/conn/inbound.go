package conn

import (
	"context"

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
// (ruling fork 12 c). A hook must not block: it runs on the stream's
// sending goroutine. A kind with no hook is logged and ignored. A hook
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
			hooked, taken = true, hook(s.epoch, b.Command)
		}
		if id := b.Command.GetCommandId().GetValue(); printable(id, maxIDBytes) {
			args = append(args, "command_id", id)
		}
	case *agentv1.ConnectResponse_Settings:
		kind = "settings"
		if hook := l.opts.Settings; hook != nil {
			hooked, taken = true, hook(s.epoch, b.Settings)
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

// capabilities is what Hello advertises: the hook's list without the
// reserved exec.raw, an invalid entry (no name, a name outside
// [a-z0-9._-] or over 64 bytes, version 0) or a repeated name, each
// dropped with connect.capability_dropped{reason, name}.
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
			args := []any{"reason", reason}
			if capabilityName(name) {
				args = append(args, "name", name)
			}
			l.log.WarnContext(ctx, "connect.capability_dropped", args...)
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
