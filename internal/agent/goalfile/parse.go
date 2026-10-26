package goalfile

import (
	"strconv"
	"strings"
	"unicode/utf8"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// maxLineBytes caps the first and CONSENT: lines (snapshot.proto GoalState,
// 03k: 4 KiB each). Both lines are untrusted text.
const maxLineBytes = 4096

// maxTokenBytes caps OWNER= and ACTION= (snapshot.proto GoalState: each
// capped at 256 bytes).
const maxTokenBytes = 256

// File is one parsed goal file: its first line, its CONSENT: line and the
// state of the first line (01d, 03c "Default goal text"). WatchPID, Owner
// and Action hold the parsed WATCH=/OWNER=/ACTION= tokens; whether the
// watcher is alive is the caller's check (a dead watcher reads stuck).
type File struct {
	FirstLine   string
	ConsentLine string
	State       typev1.GoalLineState
	WatchPID    uint32
	Owner       string
	Action      string
}

// Parse reads the first line and the first CONSENT: line of data. The
// grammar is the goal text of 03c: DONE ends the goal; a BLOCKED line with
// WATCH=<pid> waits on outside work; one with OWNER=<who> ACTION=<what>
// waits on a person; any other BLOCKED line is stuck, and so is a line
// whose WATCH= is not a pid or that names an owner without an action: the
// agent never guesses. Anything else is working, including an empty file.
func Parse(data []byte) File {
	s := string(data)
	first := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first = s[:i]
	}
	var consent string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "CONSENT:") {
			consent = line
			break
		}
	}
	f := File{FirstLine: clean(first), ConsentLine: clean(consent)}
	if f.FirstLine == "DONE" {
		f.State = typev1.GoalLineState_GOAL_LINE_STATE_DONE
		return f
	}
	if !strings.HasPrefix(f.FirstLine, "BLOCKED") {
		f.State = typev1.GoalLineState_GOAL_LINE_STATE_WORKING
		return f
	}
	var watch string
	for _, tok := range strings.Fields(f.FirstLine) {
		switch {
		case strings.HasPrefix(tok, "WATCH="):
			watch = strings.TrimPrefix(tok, "WATCH=")
		case strings.HasPrefix(tok, "OWNER="):
			f.Owner = cut(strings.TrimPrefix(tok, "OWNER="), maxTokenBytes)
		case strings.HasPrefix(tok, "ACTION="):
			f.Action = cut(strings.TrimPrefix(tok, "ACTION="), maxTokenBytes)
		}
	}
	switch {
	case watch != "":
		n, err := strconv.ParseUint(watch, 10, 32)
		if err != nil {
			f.State = typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK
			break
		}
		f.WatchPID = uint32(n)
		f.State = typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH
	case f.Owner != "" && f.Action != "":
		f.State = typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER
	default:
		f.State = typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK
	}
	return f
}

// clean makes untrusted goal text safe to report (02e, as inventory's
// cleanText): invalid UTF-8 and every control character (C0, DEL, C1;
// lines are one line, so newline and tab too) are dropped, then the text
// is cut to maxLineBytes on a rune boundary.
func clean(s string) string { return cut(s, maxLineBytes) }

// Clean caps any untrusted hook or file text the same way, to limit
// bytes: the registry's harness session ids are cleaned at this
// boundary, never logged raw.
func Clean(s string, limit int) string { return cut(s, limit) }

// cut drops control characters like clean and cuts to limit bytes on a
// rune boundary.
func cut(s string, limit int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
