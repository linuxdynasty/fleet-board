package api

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// applyMaskedFields copies the masked paths of src onto a clone of
// stored. Every path passed CheckFieldMask against the RPC's allowlist;
// prefix is the leading segment ("spec"). A masked field takes the
// request's value as is, including unset: clearing a layer's override
// returns it to inheritance. An unknown path is the hub's own failure:
// the mask check should have refused it.
func applyMaskedFields(stored, src proto.Message, paths []string, prefix string) (proto.Message, error) {
	out := proto.Clone(stored)
	for _, p := range paths {
		rest, ok := strings.CutPrefix(p, prefix+".")
		if !ok || rest == "" {
			return nil, fmt.Errorf("api: masked path %q is outside %q", p, prefix)
		}
		if err := setMaskedField(out.ProtoReflect(), src.ProtoReflect(), strings.Split(rest, ".")); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// setMaskedField copies one dotted field path from src to dst. Message
// segments are walked (made mutable); the leaf is cleared, then set when
// the request holds it.
func setMaskedField(dst, src protoreflect.Message, segs []string) error {
	fd := dst.Descriptor().Fields().ByName(protoreflect.Name(segs[0]))
	if fd == nil {
		return fmt.Errorf("api: masked field %q names nothing", segs[0])
	}
	if len(segs) == 1 {
		dst.Clear(fd)
		if src.Has(fd) {
			dst.Set(fd, src.Get(fd))
		}
		return nil
	}
	if fd.Message() == nil {
		return fmt.Errorf("api: masked field %q walks into a scalar", segs[0])
	}
	return setMaskedField(dst.Mutable(fd).Message(), src.Get(fd).Message(), segs[1:])
}
