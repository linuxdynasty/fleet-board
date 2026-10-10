package tmux

import (
	"bytes"
	"math"
	"strconv"
	"time"
)

// maxNameBytes bounds a notification name kept in [Unknown]; tmux's longest
// is 23 bytes ("unlinked-window-renamed").
const maxNameBytes = 64

// guard is the "time number flags" of a %begin, %end or %error line.
type guard struct {
	time   int64
	number uint64
	flags  int
}

// parseGuard reads exactly three unsigned decimal numbers split by single
// spaces.
func parseGuard(args []byte) (guard, bool) {
	f := bytes.Split(args, []byte(" "))
	if len(f) != 3 || !digits(f[0]) || !digits(f[1]) || !digits(f[2]) {
		return guard{}, false
	}
	t, err1 := strconv.ParseInt(string(f[0]), 10, 64)
	n, err2 := strconv.ParseUint(string(f[1]), 10, 64)
	fl, err3 := strconv.ParseInt(string(f[2]), 10, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return guard{}, false
	}
	return guard{time: t, number: n, flags: int(fl)}, true
}

// parseNotification reads one notification line (it starts with "%"). It
// returns the message, or the error class when the line is not in tmux's
// form. Names this parser does not read become [Unknown].
func parseNotification(line []byte) (Message, ErrKind) {
	name, rest, hasRest := bytes.Cut(line[1:], []byte(" "))
	if !validName(name) {
		return nil, ErrBadNotification
	}
	switch string(name) {
	case "output":
		pane, value, ok := bytes.Cut(rest, []byte(" "))
		id, idOK := parseID(pane, '%')
		if !hasRest || !ok || !idOK {
			return nil, ErrBadNotification
		}
		data, ok := decodeOutput(value)
		if !ok {
			return nil, ErrBadEscape
		}
		return Output{Pane: PaneID(id), Data: data}, ""
	case "extended-output":
		return parseExtendedOutput(rest, hasRest)
	case "pause", "continue":
		id, ok := parseID(rest, '%')
		if !hasRest || !ok {
			return nil, ErrBadNotification
		}
		if string(name) == "pause" {
			return Pause{Pane: PaneID(id)}, ""
		}
		return Continue{Pane: PaneID(id)}, ""
	case "exit":
		return Exit{Reason: string(rest)}, ""
	case "subscription-changed":
		return parseSubscription(rest, hasRest)
	case "window-add", "unlinked-window-add", "unlinked-window-close":
		id, ok := parseID(rest, '@')
		if !hasRest || !ok {
			return nil, ErrBadNotification
		}
		if string(name) == "unlinked-window-close" {
			return WindowClose{Window: WindowID(id), Unlinked: true}, ""
		}
		return WindowAdd{Window: WindowID(id), Unlinked: string(name) == "unlinked-window-add"}, ""
	case "window-renamed":
		w, label, ok := idAndName(rest, hasRest, '@')
		if !ok {
			return nil, ErrBadNotification
		}
		return WindowRenamed{Window: WindowID(w), Name: label}, ""
	case "sessions-changed":
		if hasRest {
			return nil, ErrBadNotification
		}
		return SessionsChanged{}, ""
	case "session-changed":
		s, label, ok := idAndName(rest, hasRest, '$')
		if !ok {
			return nil, ErrBadNotification
		}
		return SessionChanged{Session: SessionID(s), Name: label}, ""
	default:
		return Unknown{Name: string(name)}, ""
	}
}

// parseExtendedOutput reads "%pane age [future args...] : value".
func parseExtendedOutput(rest []byte, hasRest bool) (Message, ErrKind) {
	f, value, ok := fieldsToColon(rest)
	if !hasRest || !ok || len(f) < 2 {
		return nil, ErrBadNotification
	}
	pane, okPane := parseID(f[0], '%')
	if !okPane || !digits(f[1]) || len(f[1]) > 13 {
		return nil, ErrBadNotification
	}
	ms, err := strconv.ParseInt(string(f[1]), 10, 64)
	if err != nil || ms > math.MaxInt64/int64(time.Millisecond) {
		return nil, ErrBadNotification
	}
	data, ok := decodeOutput(value)
	if !ok {
		return nil, ErrBadEscape
	}
	return Output{Pane: PaneID(pane), Data: data, Extended: true, Age: time.Duration(ms) * time.Millisecond}, ""
}

// parseSubscription reads "name $s @w index %p [future args...] : value".
// The client must never use a name with a space: tmux accepts one, and the
// line is then misread (here as a bad notification).
func parseSubscription(rest []byte, hasRest bool) (Message, ErrKind) {
	f, value, ok := fieldsToColon(rest)
	if !hasRest || !ok || len(f) < 5 || len(f[0]) == 0 {
		return nil, ErrBadNotification
	}
	s, ok1 := parseOptionalID(f[1], '$')
	w, ok2 := parseOptionalID(f[2], '@')
	idx, ok3 := parseOptionalID(f[3], 0)
	pn, ok4 := parseOptionalID(f[4], '%')
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, ErrBadNotification
	}
	return SubscriptionChanged{
		Name: string(f[0]), Session: SessionID(s), Window: WindowID(w),
		WindowIndex: idx, Pane: PaneID(pn), Value: string(value),
	}, ""
}

// idAndName reads "<id> <name>", where the name is everything after the first
// space, kept byte for byte (it can be empty or hold spaces).
func idAndName(rest []byte, hasRest bool, prefix byte) (int, string, bool) {
	idPart, label, ok := bytes.Cut(rest, []byte(" "))
	id, idOK := parseID(idPart, prefix)
	if !hasRest || !ok || !idOK {
		return 0, "", false
	}
	return id, string(label), true
}

// fieldsToColon splits rest at single spaces up to the first field that is a
// lone ":" and returns the fields before it and everything after ": ".
func fieldsToColon(rest []byte) (fields [][]byte, value []byte, ok bool) {
	for len(rest) > 0 {
		field, after, more := bytes.Cut(rest, []byte(" "))
		if string(field) == ":" {
			return fields, after, true
		}
		if !more {
			return nil, nil, false
		}
		fields = append(fields, field)
		rest = after
	}
	return nil, nil, false
}

// parseOptionalID reads an id, or "-" as [NoID]. A prefix of 0 means a bare
// number (the window index).
func parseOptionalID(b []byte, prefix byte) (int, bool) {
	if string(b) == "-" {
		return NoID, true
	}
	return parseID(b, prefix)
}

// parseID reads prefix followed by 1 to 10 digits that fit tmux's 32-bit ids.
func parseID(b []byte, prefix byte) (int, bool) {
	if prefix != 0 {
		if len(b) == 0 || b[0] != prefix {
			return 0, false
		}
		b = b[1:]
	}
	if !digits(b) || len(b) > 10 {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b), 10, 32)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

// digits reports a non-empty run of ASCII digits.
func digits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validName reports a tmux notification name: a lower-case letter, then
// lower-case letters, digits and hyphens, at most maxNameBytes.
func validName(b []byte) bool {
	if len(b) == 0 || len(b) > maxNameBytes || b[0] < 'a' || b[0] > 'z' {
		return false
	}
	for _, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// decodeOutput undoes tmux's output escaping: a backslash and three octal
// digits stand for one byte; every other byte is itself. Anything else after
// a backslash is not tmux's form.
func decodeOutput(v []byte) ([]byte, bool) {
	out := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		if len(v)-i < 4 {
			return nil, false
		}
		n := 0
		for _, d := range v[i+1 : i+4] {
			if d < '0' || d > '7' {
				return nil, false
			}
			n = n*8 + int(d-'0')
		}
		if n > 0xff {
			return nil, false
		}
		out = append(out, byte(n))
		i += 3
	}
	return out, true
}
