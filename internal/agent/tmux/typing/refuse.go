package typing

import (
	"errors"
	"unicode"
	"unicode/utf8"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Limits on what one line may carry.
const (
	// MaxLiteralBytes is the cap on literal text: KeyInput.literal is 4 KiB
	// (02e, "Size caps"), counted in bytes.
	MaxLiteralBytes = 4096
	// MaxKeys is the most named keys one line types; a menu answer needs a
	// few.
	MaxKeys = 32
	// maxPaneDigits bounds the number in a "%<n>" pane id.
	maxPaneDigits = 10
	// maxStartID bounds PaneKey.server_start_id.
	maxStartID = 64
)

// Reason is why a constructor refused: the closed value set of the `reason`
// label of fleet_agent_typing_refused_total and of the `reason` field of the
// log event typing.refused (05b).
type Reason string

// The reasons.
const (
	ReasonPurpose Reason = "purpose"      // no purpose, or one outside the set
	ReasonPane    Reason = "pane"         // no PaneKey, an unknown server, a bad start id, or a pane id not "%<digits>"
	ReasonEmpty   Reason = "empty"        // no text, or no keys
	ReasonTooLong Reason = "too_long"     // text over MaxLiteralBytes, or keys over MaxKeys
	ReasonNewline Reason = "newline"      // a "\n" or "\r" in the text: either one is Enter
	ReasonControl Reason = "control_char" // another control character (ESC, NUL, DEL, C1, tab)
	ReasonUTF8    Reason = "invalid_utf8" // text that is not UTF-8
	ReasonKey     Reason = "key"          // a key outside the closed set
)

// RefusedError is the error every refused constructor returns. It carries
// the reason only, never the text or the pane it was given.
type RefusedError struct {
	Reason Reason
}

func (e *RefusedError) Error() string { return "typing: refused: " + string(e.Reason) }

// ErrZeroLine is what Command returns for a Line no constructor built. Its
// type is stated because the surface test refuses an exported var whose
// type is left to the compiler (it could hide a Line).
var ErrZeroLine error = errors.New("typing: the zero Line types nothing")

// serverKinds are the tmux servers a line may target (PaneKey.server, ADR
// 0011), with the `server_kind` word typing.sent logs for each.
var serverKinds = map[string]string{
	"default":     "default",
	"fleet":       "fleet",
	"fleet-probe": "probe",
}

// checkPane accepts a whole PaneKey only (row-7 ruling fork 11): a known
// server, a server start id in the [a-z0-9-] alphabet of ids.proto, and
// tmux's own pane id form. The start id lets the client refuse a line built
// for a server that has since restarted and reused the pane id. A session
// or window target could reach a pane nobody chose; a space, ";" or newline
// could end the target and start something else.
func checkPane(pane *typev1.PaneKey) *RefusedError {
	if _, ok := serverKinds[pane.GetServer()]; !ok {
		return &RefusedError{ReasonPane}
	}
	start := pane.GetServerStartId()
	if start == "" || len(start) > maxStartID {
		return &RefusedError{ReasonPane}
	}
	for i := 0; i < len(start); i++ {
		if c := start[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return &RefusedError{ReasonPane}
		}
	}
	id := pane.GetPaneId()
	if len(id) < 2 || len(id) > 1+maxPaneDigits || id[0] != '%' {
		return &RefusedError{ReasonPane}
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return &RefusedError{ReasonPane}
		}
	}
	return nil
}

// checkText accepts printable UTF-8 only. Control characters are refused,
// not escaped: "\r" or "\n" typed into a terminal is Enter, ESC starts a
// sequence the harness reads (it can end a bracketed paste), and C-c is a
// named key. Keys that need them go through NewKeys. Strict is the safe
// default and can be loosened later; loose cannot be taken back.
func checkText(text string) *RefusedError {
	switch {
	case text == "":
		return &RefusedError{ReasonEmpty}
	case len(text) > MaxLiteralBytes:
		return &RefusedError{ReasonTooLong}
	case !utf8.ValidString(text):
		return &RefusedError{ReasonUTF8}
	}
	for _, r := range text {
		switch {
		case r == '\n' || r == '\r':
			return &RefusedError{ReasonNewline}
		case unicode.IsControl(r):
			return &RefusedError{ReasonControl}
		}
	}
	return nil
}

func checkKeys(keys []Key) *RefusedError {
	switch {
	case len(keys) == 0:
		return &RefusedError{ReasonEmpty}
	case len(keys) > MaxKeys:
		return &RefusedError{ReasonTooLong}
	}
	for _, k := range keys {
		if !k.valid() {
			return &RefusedError{ReasonKey}
		}
	}
	return nil
}
