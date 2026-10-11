package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Tags are the @fleet_* user options tmux holds for a pane, as a format
// finds them from that pane (pane, then window, then session options:
// row-7 ruling forks 6 and 7). Values are raw and untrusted: anyone who can
// run tmux can set them, and one can hold a newline or a tab (the caller
// cleans them). A value over MaxFieldBytes reads as empty.
type Tags struct {
	// AttachView is whether @fleet_internal is exactly "attach_view": the
	// creator's mark of an internal session (02h). The value is not read.
	AttachView bool
	SessionID  string // @fleet_session_id (FLEET_SESSION_ID)
	Owner      string // @fleet_owner (FLEET_OWNER)
	PaneKey    string // @fleet_pane_key (FLEET_PANE_KEY)
	GoalID     string // @fleet_goal_id (FLEET_GOAL_ID)
}

// SessionInfo is one session in an [Inventory]. Name is the developer's
// text: never logged.
type SessionInfo struct {
	ID      SessionID
	Name    string
	Created time.Time
	// Clients is how many clients are attached, without the agent's own
	// control client.
	Clients int
	// Tags are the session's, as read from its first pane.
	Tags Tags
}

// PaneInfo is one pane in an [Inventory], with its window. WindowName and
// Command are the developer's text: never logged.
type PaneInfo struct {
	Pane
	WindowIndex  int
	WindowName   string
	WindowActive bool
	AutoRename   bool
	Index        int
	Active       bool
	Command      string // pane_current_command
	Tags         Tags
}

// Inventory is every session, window and pane on the server, read by one
// list-panes -a (tmux runs one command at a time, so it is one moment).
type Inventory struct {
	// StartID is the server start the read came from: the client checks it
	// did not change while the command ran.
	StartID  string
	Sessions []SessionInfo // by session id
	// Panes are in tmux's order; a pane of a window linked into several
	// sessions (a grouped session) is listed once for each.
	Panes []PaneInfo
}

// ReadInventory lists every session, window and pane with one
// list-panes -a. A server that restarted, or an attach that ended, while
// it ran is ErrNotAttached; a reply cut short or not in the asked shape is
// ErrBadReply (nothing partial is returned). A text field over
// MaxFieldBytes, or not the length tmux gave, reads as empty and is counted
// in the parser's Metrics; it never fails the read.
func (c *Client) ReadInventory(ctx context.Context) (Inventory, error) {
	c.mu.Lock()
	// The session the control client sits on is read with the conn, before
	// the command: tmux counts the client there when it runs the command.
	cn, start, own := c.conn, c.startID, c.session
	up := c.state == StateUp
	c.mu.Unlock()
	if cn == nil || !up {
		return Inventory{}, ErrNotAttached
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails (crypto/rand)
	marker := "~" + hex.EncodeToString(raw[:]) + "~"
	r, err := c.on(ctx, cn, "list-panes -a -F '"+inventoryFormat(marker)+"'", NoID)
	if err != nil {
		return Inventory{}, fmt.Errorf("tmux: inventory: %w", err)
	}
	c.mu.Lock()
	same := c.conn == cn && c.startID == start
	c.mu.Unlock()
	if !same {
		return Inventory{}, ErrNotAttached
	}
	if r.Dropped > 0 {
		return Inventory{}, fmt.Errorf("tmux: inventory: %w", ErrBadReply)
	}
	inv := Inventory{StartID: start}
	seen := map[SessionID]bool{}
	// A window linked into several sessions (a grouped session) lists its
	// panes once per session, and one linked twice into the same session
	// (link-window) once per index: that repeat is dropped, so the window is
	// reported once, at its first index (tmux lists indexes in order). A
	// pane listed in two windows of one session is not tmux's shape.
	type where struct {
		s SessionID
		p PaneID
	}
	panes := map[where]WindowID{}
	var drops []fieldDrop
	for _, line := range joinRecords(r.Body, marker) {
		s, p, dropped, ok := parseInventoryLine(line, marker)
		if !ok {
			return Inventory{}, fmt.Errorf("tmux: inventory: %w", ErrBadReply)
		}
		if w, dup := panes[where{s.ID, p.ID}]; dup {
			if w != p.Window {
				return Inventory{}, fmt.Errorf("tmux: inventory: %w", ErrBadReply)
			}
			continue // the same window at a second index
		}
		panes[where{s.ID, p.ID}] = p.Window
		drops = append(drops, dropped...)
		if !seen[s.ID] {
			seen[s.ID] = true
			if s.ID == own && s.Clients > 0 {
				s.Clients-- // the agent's own control client
			}
			inv.Sessions = append(inv.Sessions, s)
		}
		inv.Panes = append(inv.Panes, p)
	}
	slices.SortFunc(inv.Sessions, func(a, b SessionInfo) int { return int(a.ID) - int(b.ID) })
	for _, d := range drops { // a read that failed reports none: its error says why
		c.opts.Parser.Metrics.fieldDropped(d)
	}
	return inv, nil
}

// joinRecords puts back records that a value broke across lines: tmux can
// write a newline in a text value raw (an option value, or a program's
// name in the last field), and a value ending in one puts the next field,
// marker first, at the start of a line. So a record takes lines until it
// holds one marker per field plus the closing marker after the last field;
// the marker is random per read, so no value can hold one and counting them
// is safe. The length tmux sends beside each text field then checks the
// join (a newline is one byte).
func joinRecords(body []string, marker string) []string {
	out := make([]string, 0, len(body))
	for _, line := range body {
		if n := len(out); n > 0 && strings.Count(out[n-1], marker) < recordMarkers {
			out[n-1] += "\n" + line
			continue
		}
		out = append(out, line)
	}
	return out
}

// parseInventoryLine reads one list-panes record of inventoryFields, with
// the text fields it left blank. The record starts with a marker and ends
// with the closing one; only those, the fixed fields and the canary can
// make it not in the asked shape (ok false).
func parseInventoryLine(line, marker string) (SessionInfo, PaneInfo, []fieldDrop, bool) {
	f := strings.Split(line, marker)
	if len(f) != recordMarkers+1 || f[0] != "" || f[len(f)-1] != "" {
		return SessionInfo{}, PaneInfo{}, nil, false
	}
	f = f[1 : len(f)-1]
	sid, ok1 := parseID([]byte(f[fixSessionID]), '$')
	created, err1 := strconv.ParseInt(f[fixCreated], 10, 64)
	attached, err2 := strconv.Atoi(f[fixAttached])
	wid, ok2 := parseID([]byte(f[fixWindowID]), '@')
	widx, err3 := strconv.Atoi(f[fixWindowIndex])
	pid, ok3 := parseID([]byte(f[fixPaneID]), '%')
	pidx, err4 := strconv.Atoi(f[fixPaneIndex])
	w, err5 := strconv.Atoi(f[fixWidth])
	h, err6 := strconv.Atoi(f[fixHeight])
	flags := []string{f[fixWindowActive], f[fixAutoRename], f[fixPaneActive], f[fixDead], f[fixAttachView]}
	if !ok1 || !ok2 || !ok3 || firstErr(err1, err2, err3, err4, err5, err6) != nil || !allFlags(flags) || attached < 0 || f[canaryAt] != "1" {
		return SessionInfo{}, PaneInfo{}, nil, false
	}
	dead := f[fixDead] == "1"
	status, signal, ok := deadFields(dead, f[fixDeadStatus], f[fixDeadSignal])
	if !ok {
		return SessionInfo{}, PaneInfo{}, nil, false
	}
	var text [len(textFields)]string
	var drops []fieldDrop
	for i := range textFields {
		v, drop, ok := boundedText(i, f[firstLength+i], f[firstText+i])
		if !ok {
			return SessionInfo{}, PaneInfo{}, nil, false
		}
		if drop != nil {
			drops = append(drops, *drop)
		}
		text[i] = v
	}
	tags := Tags{
		AttachView: f[fixAttachView] == "1",
		SessionID:  text[textSessionID], Owner: text[textOwner], PaneKey: text[textPaneKey], GoalID: text[textGoalID],
	}
	s := SessionInfo{ID: SessionID(sid), Name: text[textSessionName], Created: time.Unix(created, 0), Clients: attached, Tags: tags}
	p := PaneInfo{
		Pane: Pane{
			Session: SessionID(sid), Window: WindowID(wid), ID: PaneID(pid),
			Dead: dead, DeadStatus: status, DeadSignal: signal, Width: w, Height: h,
		},
		WindowIndex: widx, WindowName: text[textWindowName], WindowActive: f[fixWindowActive] == "1", AutoRename: f[fixAutoRename] == "1",
		Index: pidx, Active: f[fixPaneActive] == "1", Command: text[textCommand], Tags: tags,
	}
	return s, p, drops, true
}

func allFlags(fs []string) bool {
	for _, f := range fs {
		if f != "0" && f != "1" {
			return false
		}
	}
	return true
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
