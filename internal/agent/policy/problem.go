package policy

import (
	"fmt"
	"strings"
	"unicode"
)

// Problem codes: a closed set, safe to log, count and audit.
const (
	CodeUnreadable    = "unreadable"
	CodeTooLarge      = "too_large"
	CodeBadYAML       = "bad_yaml"
	CodeBadType       = "bad_type"
	CodeUnknownKey    = "unknown_key"
	CodeDuplicateKey  = "duplicate_key"
	CodeOutOfRange    = "out_of_range"
	CodeBadValue      = "bad_value"
	CodeSchemaVersion = "schema_version"
)

// maxErrorBytes is the PolicyInfo.error cap (snapshot.proto).
const maxErrorBytes = 256

// Problem is why a policy file puts the agent in safe mode. Its text is built
// only from a line number, a key name the policy knows and a fixed reason, so
// it never carries file contents (PolicyInfo.error, 03e).
type Problem struct {
	// Code is one of the Code constants.
	Code string
	// Line is the 1-based line in the file; 0 when none applies.
	Line int
	text string
}

// Error is the one-line reason, at most 256 bytes.
func (p *Problem) Error() string { return p.text }

// problem builds a Problem. what is a fixed phrase; scope is the name of a
// key the policy defines (or empty); line is the YAML line.
func problem(code string, line int, scope, what string) *Problem {
	var b strings.Builder
	if line > 0 {
		fmt.Fprintf(&b, "line %d: ", line)
	}
	if scope != "" {
		b.WriteString(scope)
		b.WriteString(": ")
	}
	b.WriteString(what)
	return &Problem{Code: code, Line: line, text: cleanLine(b.String())}
}

// cleanLine keeps printable ASCII only and caps the length, so even a mistake
// in a caller cannot put a control character or a long text on the wire.
func cleanLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > unicode.MaxASCII || unicode.IsControl(r) {
			r = '?'
		}
		b.WriteRune(r)
		if b.Len() >= maxErrorBytes {
			break
		}
	}
	return b.String()
}
