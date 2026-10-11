package tmux

import (
	"strconv"
	"strings"
	"time"
)

// NoID is the value of a [SessionID], [WindowID], [PaneID] or window index
// that tmux wrote as "-": the line does not name one.
const NoID = -1

// SessionID is a tmux session id, written "$N". tmux ids restart from zero
// with a new server, so an id alone never names a session across restarts.
type SessionID int

// WindowID is a tmux window id, written "@N".
type WindowID int

// PaneID is a tmux pane id, written "%N".
type PaneID int

func (id SessionID) String() string { return idString('$', int(id)) }
func (id WindowID) String() string  { return idString('@', int(id)) }
func (id PaneID) String() string    { return idString('%', int(id)) }

func idString(prefix byte, n int) string {
	if n < 0 {
		return "-"
	}
	return string(prefix) + strconv.Itoa(n)
}

// Message is one thing the [Parser] read from a control-mode stream: a
// [Reply], [Output], [Pause], [Continue], [Exit], [SubscriptionChanged],
// [WindowAdd], [WindowClose], [WindowRenamed], [SessionChanged],
// [SessionsChanged], [Unknown] or [Discarded]. The set is closed; a type switch over it is the
// way to read one.
type Message interface {
	message()
}

// Reply is one command's answer: the lines between "%begin" and its "%end"
// (Failed false) or "%error" (Failed true). Body lines are tmux's raw command
// output, which can be pane text (capture-pane): data, never instructions, and
// never logged.
type Reply struct {
	// Time is the guard's time (seconds since the epoch), Number the
	// server's command number and Flags the guard flags (1 when this client
	// sent the command).
	Time   int64
	Number uint64
	Flags  int
	Failed bool
	// Body holds the output lines without their newlines, in order. A line
	// can start with "%": only an exact closing guard ends a reply.
	Body []string
	// Dropped counts body lines lost to the line or reply bound; a reply
	// with Dropped > 0 is incomplete and must not be trusted as whole.
	Dropped int
}

// ReplyLineOverhead is what each kept body line costs against
// Options.MaxReplyBytes beyond its own bytes: its newline and the 16-byte
// string header that holds it in Body. An empty line is not free.
const ReplyLineOverhead = 17

// ControlNotes returns the body lines that are exactly a well-formed
// "%pause %N" or "%continue %N", as [Pause] and [Continue] messages in
// order. tmux writes them inside the reply to the client's own
// "refresh-client -A '%N:pause'" or "'%N:continue'", but any other reply
// (a capture of a pane) can hold the same text. A Pause found in any reply
// is safe to honour (pausing is the safe direction, and tmux may pause a
// pane while another command runs); a Continue is honoured only for a reply
// the client knows answers its own refresh-client -A for that pane, so a
// pane is never marked resumed on a Continue the client did not ask for.
// The body itself is never changed.
func (r Reply) ControlNotes() []Message {
	var out []Message
	for _, line := range r.Body {
		if !strings.HasPrefix(line, "%pause ") && !strings.HasPrefix(line, "%continue ") {
			continue
		}
		if m, kind := parseNotification([]byte(line)); kind == "" {
			out = append(out, m)
		}
	}
	return out
}

// Output is "%output" or, once pause-after is set, "%extended-output": bytes
// a pane wrote. Data is decoded (tmux's \ooo escapes undone) and is pane
// text: never logged, never a metric label.
type Output struct {
	Pane PaneID
	Data []byte
	// Extended is true for "%extended-output"; Age is then how long tmux
	// held the bytes before sending them.
	Extended bool
	Age      time.Duration
}

// Pause is "%pause": tmux stopped sending a pane's output to this client
// (pause-after) until the client continues it.
type Pause struct{ Pane PaneID }

// Continue is "%continue": a paused pane's output flows again. tmux writes
// it inside the reply to the client's own "refresh-client -A '%N:continue'"
// (01 E2); there it stays in Reply.Body (see [Reply.ControlNotes]).
type Continue struct{ Pane PaneID }

// Exit is "%exit": the control client is ending. Reason is tmux's text and
// may name a session (the developer's text): never logged.
type Exit struct{ Reason string }

// SubscriptionChanged is "%subscription-changed": a format set with
// "refresh-client -B" has a new value. Session, Window, WindowIndex and Pane
// are [NoID] where tmux wrote "-". Value is the format's output byte for byte
// (spaces kept, nothing trimmed or decoded).
type SubscriptionChanged struct {
	Name        string
	Session     SessionID
	Window      WindowID
	WindowIndex int
	Pane        PaneID
	Value       string
}

// WindowAdd is "%window-add" (a window linked to this client's session) or,
// with Unlinked, "%unlinked-window-add" (a window made in another session).
type WindowAdd struct {
	Window   WindowID
	Unlinked bool
}

// WindowClose is "%unlinked-window-close": a window of another session
// closed. "%window-close" is not read yet and comes as [Unknown], so
// Unlinked is always true for now.
type WindowClose struct {
	Window   WindowID
	Unlinked bool
}

// WindowRenamed is "%window-renamed". Name is the developer's text, byte for
// byte (spaces kept): never logged, never a metric label.
type WindowRenamed struct {
	Window WindowID
	Name   string
}

// SessionChanged is "%session-changed": this client is now attached to
// Session. Name is the developer's text: never logged, never a label.
type SessionChanged struct {
	Session SessionID
	Name    string
}

// SessionsChanged is "%sessions-changed": a session was created or
// destroyed somewhere on the server. Every control client gets it, whatever
// session it is attached to. It carries nothing; the client lists the
// sessions to learn what changed.
type SessionsChanged struct{}

// Unknown is a notification this parser does not read (for example
// "%window-close" or "%session-renamed"). Only the name is kept: the rest of
// the line can hold the developer's text.
type Unknown struct{ Name string }

// Discarded is a line, or part of a stream, the parser could not use outside
// a reply. It carries the closed error class and the byte count only, never
// the bytes.
type Discarded struct {
	ErrKind ErrKind
	Bytes   int
}

func (Reply) message()               {}
func (Output) message()              {}
func (Pause) message()               {}
func (Continue) message()            {}
func (Exit) message()                {}
func (SubscriptionChanged) message() {}
func (WindowAdd) message()           {}
func (WindowClose) message()         {}
func (WindowRenamed) message()       {}
func (SessionChanged) message()      {}
func (SessionsChanged) message()     {}
func (Unknown) message()             {}
func (Discarded) message()           {}

// ErrKind is the closed class of a parse failure: the err_kind field of
// tmux.parse_error and the err_kind label of
// fleet_agent_tmux_parse_errors_total (05b). [ErrLineTooLong] is counted
// apart, in fleet_agent_tmux_lines_too_long_total.
type ErrKind string

// The parse failure classes.
const (
	// ErrLineTooLong: a line passed Options.MaxLineBytes; it was skipped to
	// its newline.
	ErrLineTooLong ErrKind = "line_too_long"
	// ErrPartialLine: the stream ended in the middle of a line.
	ErrPartialLine ErrKind = "partial_line"
	// ErrStrayLine: a line outside any reply that is not a notification.
	ErrStrayLine ErrKind = "stray_line"
	// ErrBadGuard: a %begin, or a %end or %error outside a reply, that is
	// not three numbers.
	ErrBadGuard ErrKind = "bad_guard"
	// ErrUnmatchedGuard: a well-formed %end or %error outside any reply.
	ErrUnmatchedGuard ErrKind = "unmatched_guard"
	// ErrBadNotification: a notification name or its arguments are not in
	// tmux's form.
	ErrBadNotification ErrKind = "bad_notification"
	// ErrBadEscape: an output value with a backslash that is not \ooo.
	ErrBadEscape ErrKind = "bad_escape"
	// ErrTruncatedReply: the stream ended inside a reply.
	ErrTruncatedReply ErrKind = "truncated_reply"
	// ErrReplyTooLarge: a reply's body passed Options.MaxReplyBytes; the
	// lines after it were dropped (counted once per reply).
	ErrReplyTooLarge ErrKind = "reply_too_large"
)
