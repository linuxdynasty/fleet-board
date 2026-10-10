package policy

import (
	"sync"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// commandKinds are the names of the arms of Command.kind, read from the
// generated descriptor so the list has one home (the proto), not a copy here:
// "KillWindow", "Drain" and so on, as settings.proto names them.
var commandKinds = sync.OnceValue(func() map[string]bool {
	kinds := map[string]bool{}
	fields := (&agentv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	for i := range fields.Len() {
		kinds[string(fields.Get(i).Message().Name())] = true
	}
	return kinds
})

// IsCommandKind reports whether name is a command kind of fleet.agent.v1.
func IsCommandKind(name string) bool { return commandKinds()[name] }
