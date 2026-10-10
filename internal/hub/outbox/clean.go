package outbox

import (
	"strings"
	"unicode/utf8"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// Caps on agent text the outbox stores (02e "Size limits").
const (
	// MaxMessage is the cap of CommandResult.message and
	// CommandProgress.message.
	MaxMessage = 1024
	// maxStep caps CommandProgress.step, a name (the 256-byte name cap).
	maxStep = 256
)

// scrubText is the hub's scrub pass. A variable only so an internal test
// can see what reaches it.
var scrubText = scrub.Scrub

// cleaned is what cleanMessage did to one field, for telemetry.
type cleaned struct {
	text                          string
	oversize, redacted, unsettled bool
}

// cleanMessage bounds and scrubs agent free text before it is stored, on
// the caller's goroutine (the stream's ingest goroutine, never the
// writer's transaction: 04b decision 2). In this order: text over max
// bytes is cut to max on a UTF-8 boundary, before anything reads it whole
// (04b decision 3: the scrubber's cost follows the input's length, and a
// 1 MiB message would take it about 16 s); control characters but '\n'
// and '\t' are dropped (02e); then the hub's pass of the shared scrubber,
// with no home folder (the agent already wrote it as "~").
func cleanMessage(s string, max int) cleaned {
	var c cleaned
	if len(s) > max {
		s, c.oversize = cutUTF8(s, max), true
	}
	// Stripping turns each invalid byte into U+FFFD (3 bytes): cut again so
	// the scrubber never reads more than the cap.
	r := scrubText(cutUTF8(stripControl(s), max), scrub.Options{MaxBytes: max})
	c.text, c.redacted, c.unsettled = r.Text, r.Changed, r.Unsettled
	return c
}

// cleanName bounds a short name (a step): cut, no control characters. It
// is not free text, so it is not scrubbed.
func cleanName(s string) string {
	return strings.ToValidUTF8(stripControl(cutUTF8(s, maxStep)), "�")
}

// cutUTF8 is s cut to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// stripControl drops C0 and C1 control characters and DEL, keeping '\n'
// and '\t'.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		}
		return r
	}, s)
}
