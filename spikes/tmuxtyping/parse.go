package main

import (
	"strconv"
	"strings"
)

// Line is one parsed control-mode line. Kind is the notification name without
// the leading "%" ("begin", "output", "subscription-changed", ...) or "data"
// for a line inside a command reply.
type Line struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`    // subscription name
	Session string `json:"session,omitempty"` // $N or "-"
	Window  string `json:"window,omitempty"`  // @N or "-"
	Pane    string `json:"pane,omitempty"`    // %N or "-"
	Value   string `json:"value,omitempty"`   // subscription value, output bytes, or the rest of the line
	Num     int64  `json:"num,omitempty"`     // command number for begin/end/error
	Flags   int    `json:"flags,omitempty"`   // 1 when the command came from this client
}

// ParseLine classifies one line of tmux control-mode output (newline already
// removed). It never fails: a line it does not know is returned with its
// notification name as Kind and the rest in Value.
func ParseLine(s string) Line {
	if !strings.HasPrefix(s, "%") {
		return Line{Kind: "data", Value: s}
	}
	head, rest, _ := strings.Cut(s[1:], " ")
	switch head {
	case "begin", "end", "error":
		f := strings.Fields(rest)
		l := Line{Kind: head}
		if len(f) >= 2 {
			l.Num, _ = strconv.ParseInt(f[1], 10, 64)
		}
		if len(f) >= 3 {
			l.Flags, _ = strconv.Atoi(f[2])
		}
		return l
	case "output":
		pane, val, _ := strings.Cut(rest, " ")
		return Line{Kind: head, Pane: pane, Value: val}
	case "extended-output":
		pane, after, _ := strings.Cut(rest, " ")
		_, val, _ := strings.Cut(after, " : ")
		return Line{Kind: head, Pane: pane, Value: val}
	case "pause", "continue":
		return Line{Kind: head, Pane: strings.TrimSpace(rest)}
	case "subscription-changed":
		return parseSubscription(rest)
	default:
		return Line{Kind: head, Value: rest}
	}
}

// parseSubscription reads "name $s @w idx %p : value"; tmux writes "-" for a
// field the subscription does not cover.
func parseSubscription(rest string) Line {
	ids, val, ok := strings.Cut(rest, " : ")
	if !ok {
		ids, val = strings.TrimSuffix(rest, " :"), ""
	}
	f := strings.Fields(ids)
	l := Line{Kind: "subscription-changed", Value: val}
	if len(f) > 0 {
		l.Name = f[0]
	}
	if len(f) > 1 {
		l.Session = f[1]
	}
	if len(f) > 2 {
		l.Window = f[2]
	}
	if len(f) > 4 {
		l.Pane = f[4]
	}
	return l
}

// Reply is one %begin ... %end or %error block.
type Reply struct {
	Num   int64    `json:"num"`
	Flags int      `json:"flags"`
	Error bool     `json:"error"`
	Body  []string `json:"body,omitempty"`
}

// blockState groups reply lines; notifications never appear inside a block.
type blockState struct {
	open bool
	cur  Reply
}

// feed takes one parsed line and returns a finished reply, or nil while the
// line is a notification or part of an unfinished block. inBlock reports
// whether the line belonged to a block.
func (b *blockState) feed(l Line, raw string) (done *Reply, inBlock bool) {
	switch {
	case l.Kind == "begin" && !b.open:
		b.open, b.cur = true, Reply{Num: l.Num, Flags: l.Flags}
		return nil, true
	case b.open && (l.Kind == "end" || l.Kind == "error") && l.Num == b.cur.Num:
		r := b.cur
		r.Error = l.Kind == "error"
		b.open = false
		return &r, true
	case b.open:
		b.cur.Body = append(b.cur.Body, raw)
		return nil, true
	}
	return nil, false
}
