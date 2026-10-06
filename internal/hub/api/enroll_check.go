package api

import (
	"regexp"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// Request caps (02b "Request limits").
const (
	maxRequestID   = 64
	maxDisplayName = 128
	maxHostname    = 253 // the DNS limit; 02b sets none
	maxLabels      = 32
	maxViolations  = 16
)

var (
	// labelName is the name rule of ids.proto for labels.
	labelName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// hubID is the format of ids.proto for hub-assigned ids.
	hubID    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	hostname = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)
)

type violations []*typev1.FieldViolation

func (v *violations) add(field string, reason typev1.ReasonCode, desc string) {
	if len(*v) < maxViolations {
		*v = append(*v, &typev1.FieldViolation{Field: field, Reason: reason, Description: desc})
	}
}

// checkEnrollRequest applies the 02b field rules and returns the token
// lifetime, or an INVALID_ARGUMENT error with one FieldViolation per bad
// field; its reason is the first violation's. Descriptions never echo the
// request.
func checkEnrollRequest(m *hubv1.CreateEnrollmentTokenRequest) (time.Duration, error) {
	var v violations
	switch id := m.GetRequestId(); {
	case id == "":
		v.add("request_id", typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required")
	case len(id) > maxRequestID:
		v.add("request_id", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "request_id is over 64 bytes")
	case !printableASCII(id):
		v.add("request_id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"request_id must be printable ASCII without spaces (a UUID or ULID)")
	}
	switch name := m.GetMachineName(); {
	case name == "":
		v.add("machine_name", typev1.ReasonCode_REASON_CODE_INVALID_NAME, "machine_name is required")
	case len(name) > maxDisplayName:
		v.add("machine_name", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "machine_name is over 128 bytes")
	case !displayText(name):
		v.add("machine_name", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"machine_name must be UTF-8 without control or invisible format characters")
	}
	switch host := m.GetExpectedHostname(); {
	case len(host) > maxHostname:
		v.add("expected_hostname", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "expected_hostname is over 253 bytes")
	case !hostname.MatchString(host):
		v.add("expected_hostname", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"expected_hostname may hold only letters, digits, '.', '-' and '_'")
	}
	if p := m.GetPreviousMachine(); p != nil {
		switch {
		case p.GetValue() == "":
			v.add("previous_machine.value", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "previous_machine names no machine")
		case !hubID.MatchString(p.GetValue()):
			v.add("previous_machine.value", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"previous_machine is 1 to 32 characters of a-z, 0-9 and '-', starting with a letter or digit")
		}
	}
	checkLabels(&v, m.GetLabels())
	ttl := ca.DefaultTokenTTL
	if d := m.GetTtl(); d != nil {
		ttl = d.AsDuration()
		if d.CheckValid() != nil || ttl <= 0 || ttl > ca.MaxTokenTTL {
			v.add("ttl", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "ttl must be more than 0 and at most 24h")
		}
	}
	if len(v) > 0 {
		return 0, apiError(connect.CodeInvalidArgument, v[0].GetReason(), "the request has invalid fields", nil, v)
	}
	return ttl, nil
}

func checkLabels(v *violations, labels []string) {
	if len(labels) > maxLabels {
		v.add("labels", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "at most 32 labels")
		return
	}
	seen := map[string]bool{}
	for i, l := range labels {
		field := "labels[" + strconv.Itoa(i) + "]"
		switch {
		case !labelName.MatchString(l):
			v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_NAME,
				"a label is 1 to 32 characters of a-z, 0-9 and '-', starting with a letter or digit")
		case seen[l]:
			v.add(field, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "a label is repeated")
		}
		seen[l] = true
	}
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// displayText refuses control and format (Cf) characters: a direction
// override or a zero-width character can make one name look like another.
func displayText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
