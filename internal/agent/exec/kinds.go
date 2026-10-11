package exec

import (
	"sync"

	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// unknownKind labels a command whose kind arm this build does not know: an
// empty oneof, or a newer field such as the reserved 99.
const unknownKind = "unknown"

// kindOneof is Command.kind, read from the generated descriptor so the list
// of kinds has one home: the proto.
var kindOneof = sync.OnceValue(func() protoreflect.OneofDescriptor {
	return (&agentv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind")
})

// Kinds lists every Command.kind arm by message name, in field order.
func Kinds() []string {
	fields := kindOneof().Fields()
	out := make([]string, 0, fields.Len())
	for i := range fields.Len() {
		out = append(out, string(fields.Get(i).Message().Name()))
	}
	return out
}

func isKind(name string) bool {
	for _, k := range Kinds() {
		if k == name {
			return true
		}
	}
	return false
}

// kindOf is c's kind arm by message name, or unknownKind.
func kindOf(c *agentv1.Command) string {
	f := c.ProtoReflect().WhichOneof(kindOneof())
	if f == nil {
		return unknownKind
	}
	return string(f.Message().Name())
}
