package typing

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Line is one tmux send-keys command, built and checked by a New…
// constructor. Its fields are unexported, so code outside this package can
// get a Line that types something only from a constructor. The zero Line
// types nothing: IsZero reports it and Command refuses it.
type Line struct {
	server  string // PaneKey.server
	startID string // PaneKey.server_start_id
	paneID  string // PaneKey.pane_id, "%12"
	purpose Purpose
	text    int // bytes of literal text; 0 for a line of named keys
	cmd     string
}

// IsZero reports a Line no constructor built. The client refuses it.
func (l Line) IsZero() bool { return l.cmd == "" }

// Command is the control-mode command line, without the newline that ends
// it, or ErrZeroLine for the zero Line. It is one send-keys command and
// nothing else: no input can add a newline or a second command to it.
func (l Line) Command() (string, error) {
	if l.IsZero() {
		return "", ErrZeroLine
	}
	return l.cmd, nil
}

// Server is the tmux server the line was built for (PaneKey.server). The
// client refuses a line whose server is not the one it is attached to.
func (l Line) Server() string { return l.server }

// ServerStartID is the server start the line was built for
// (PaneKey.server_start_id). The client refuses a line whose start id is not
// the one it is attached to, so a line never lands on a reused pane id.
func (l Line) ServerStartID() string { return l.startID }

// PaneID is the tmux pane id the line types into ("%12").
func (l Line) PaneID() string { return l.paneID }

// Purpose is the purpose the caller stated.
func (l Line) Purpose() Purpose { return l.purpose }

// NewLiteral builds a line that types text into pane exactly as written:
// `send-keys -t <pane_id> -l -- '<text>'`. The -l flag stops tmux reading
// key names in the text, "--" stops a leading "-" being read as a flag, and
// single quotes keep every byte (tmux expands nothing inside them). pane is
// a whole PaneKey (a known server, its start id and a "%<n>" pane id); the
// text is printable UTF-8 of 1 to MaxLiteralBytes bytes with no newline or
// other control character. A refusal is a *RefusedError, counted and logged
// by rec (nil counts nowhere), and returns the zero Line.
func NewLiteral(rec *Recorder, pane *typev1.PaneKey, purpose Purpose, text string) (Line, error) {
	if err := checkCommon(pane, purpose); err != nil {
		return Line{}, rec.refused(err, purpose)
	}
	if err := checkText(text); err != nil {
		return Line{}, rec.refused(err, purpose)
	}
	l := newLine(pane, purpose)
	var b strings.Builder
	b.Grow(len("send-keys -t  -l -- ''") + len(l.paneID) + len(text) + 8)
	b.WriteString("send-keys -t ")
	b.WriteString(l.paneID)
	b.WriteString(" -l -- '")
	// A quote inside the text closes the quoted part, adds an escaped quote
	// and opens a new one: ' becomes '\''.
	b.WriteString(strings.ReplaceAll(text, "'", `'\''`))
	b.WriteByte('\'')
	l.text, l.cmd = len(text), b.String()
	return l, nil
}

// NewKeys builds a line that presses the named keys in order:
// `send-keys -t <pane_id> <name>...`, the names from the closed Key set
// only, 1 to MaxKeys of them. pane and refusals are as for NewLiteral.
func NewKeys(rec *Recorder, pane *typev1.PaneKey, purpose Purpose, keys ...Key) (Line, error) {
	if err := checkCommon(pane, purpose); err != nil {
		return Line{}, rec.refused(err, purpose)
	}
	if err := checkKeys(keys); err != nil {
		return Line{}, rec.refused(err, purpose)
	}
	l := newLine(pane, purpose)
	var b strings.Builder
	b.WriteString("send-keys -t ")
	b.WriteString(l.paneID)
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k.String())
	}
	l.cmd = b.String()
	return l, nil
}

func newLine(pane *typev1.PaneKey, purpose Purpose) Line {
	return Line{
		server:  pane.GetServer(),
		startID: pane.GetServerStartId(),
		paneID:  pane.GetPaneId(),
		purpose: purpose,
	}
}

func checkCommon(pane *typev1.PaneKey, purpose Purpose) *RefusedError {
	if !purpose.valid() {
		return &RefusedError{ReasonPurpose}
	}
	return checkPane(pane)
}
