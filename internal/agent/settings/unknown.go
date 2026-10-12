package settings

import (
	"path/filepath"
	"slices"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// unknownFields lists, once each and in order, the field numbers of m that
// this agent's schema does not know (03e "Skew with an older agent").
func unknownFields(m proto.Message) []uint32 {
	if m == nil || !m.ProtoReflect().IsValid() {
		return nil
	}
	b := m.ProtoReflect().GetUnknown()
	var out []uint32
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			break
		}
		b = b[n:]
		if !slices.Contains(out, uint32(num)) { //nolint:gosec // G115: a field number is at most 2^29-1
			out = append(out, uint32(num)) //nolint:gosec // G115: as above
		}
	}
	slices.Sort(out)
	return out
}

func filepathClean(p string) string { return filepath.Clean(p) }
