package api

import (
	"strconv"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// CheckFieldMask validates an update mask against the allowlist of canonical
// dotted paths the RPC may change (02b update masks). allowed holds the full
// path set; identity and status paths stay out of it. A nil or empty mask
// names nothing to change. Every bad path gets its own FieldViolation in one
// INVALID_ARGUMENT, whose reason is the first violation's (the house field
// rule); no message echoes the request.
func CheckFieldMask(mask *fieldmaskpb.FieldMask, allowed map[string]bool) error {
	var v violations
	if mask == nil || len(mask.GetPaths()) == 0 {
		v.add("update_mask", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"update_mask names at least one field")
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	seen := map[string]bool{}
	for i, p := range mask.GetPaths() {
		field := "update_mask.paths[" + strconv.Itoa(i) + "]"
		switch {
		case !allowed[p]:
			v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"the path is not updatable through this call")
		case seen[p]:
			v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"the path is repeated")
		}
		seen[p] = true
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}
