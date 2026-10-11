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
// cleans them).
type Tags struct {
	Internal  string // @fleet_internal: the creator's mark of an internal session ("attach_view")
	SessionID string // @fleet_session_id (FLEET_SESSION_ID)
	Owner     string // @fleet_owner (FLEET_OWNER)
	PaneKey   string // @fleet_pane_key (FLEET_PANE_KEY)
	GoalID    string // @fleet_goal_id (FLEET_GOAL_ID)
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

// inventoryFields are the fields of one list-panes line, in order. The
// marker between them is new for every read, so no name or option value
// can hold it.
var inventoryFields = []string{
	"session_id", "session_created", "session_attached",
	"window_id", "window_index", "window_active", "automatic-rename",
	"pane_id", "pane_index", "pane_active", "pane_dead", "pane_dead_status", "pane_width", "pane_height",
	"@fleet_internal", "@fleet_session_id", "@fleet_owner", "@fleet_pane_key", "@fleet_goal_id",
	"session_name", "window_name", "pane_current_command",
}

// MaxFieldChars caps each text field (the names, the command and the
// @fleet_* options) in tmux itself, with #{=N:...} (tmux 3.2a and 3.7c), so
// no value can push a line past the parser's line bound or the reply past
// its bound: 22 fields of at most 256 characters (4 bytes each at worst)
// keep a pane's line near 23 KiB. The caller cuts names to their bytes.
const MaxFieldChars = 256

// firstText is the index of the first text field in inventoryFields.
const firstText = 14

func inventoryFormat(marker string) string {
	var b strings.Builder
	for i, f := range inventoryFields {
		if i >= firstText {
			f = "=" + strconv.Itoa(MaxFieldChars) + ":" + f
		}
		b.WriteString(marker + "#{" + f + "}")
	}
	return b.String()
}

// ReadInventory lists every session, window and pane with one
// list-panes -a. A server that restarted, or an attach that ended, while
// it ran is ErrNotAttached; a reply cut short or not in the asked shape is
// ErrBadReply (nothing partial is returned).
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
	for _, line := range joinRecords(r.Body, marker) {
		s, p, ok := parseInventoryLine(line, marker)
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
	return inv, nil
}

// joinRecords puts back records that a value broke across lines: tmux can
// write a newline in an option value raw, and a value ending in one puts
// the next field, marker first, at the start of a line. So a record takes
// lines until it holds one marker per field; the marker is random per read,
// so no value can hold one and counting them is safe.
func joinRecords(body []string, marker string) []string {
	out := make([]string, 0, len(body))
	for _, line := range body {
		if n := len(out); n > 0 && strings.Count(out[n-1], marker) < len(inventoryFields) {
			out[n-1] += "\n" + line
			continue
		}
		out = append(out, line)
	}
	return out
}

// parseInventoryLine reads one list-panes line of inventoryFields.
func parseInventoryLine(line, marker string) (SessionInfo, PaneInfo, bool) {
	f := strings.Split(line, marker)
	if len(f) != len(inventoryFields)+1 || f[0] != "" {
		return SessionInfo{}, PaneInfo{}, false
	}
	f = f[1:]
	sid, ok1 := parseID([]byte(f[0]), '$')
	created, err1 := strconv.ParseInt(f[1], 10, 64)
	attached, err2 := strconv.Atoi(f[2])
	wid, ok2 := parseID([]byte(f[3]), '@')
	widx, err3 := strconv.Atoi(f[4])
	pid, ok3 := parseID([]byte(f[7]), '%')
	pidx, err4 := strconv.Atoi(f[8])
	w, err5 := strconv.Atoi(f[12])
	h, err6 := strconv.Atoi(f[13])
	flags := []string{f[5], f[6], f[9], f[10]}
	if !ok1 || !ok2 || !ok3 || firstErr(err1, err2, err3, err4, err5, err6) != nil || !allFlags(flags) || attached < 0 {
		return SessionInfo{}, PaneInfo{}, false
	}
	tags := Tags{Internal: f[14], SessionID: f[15], Owner: f[16], PaneKey: f[17], GoalID: f[18]}
	s := SessionInfo{ID: SessionID(sid), Name: f[19], Created: time.Unix(created, 0), Clients: attached, Tags: tags}
	p := PaneInfo{
		Pane: Pane{
			Session: SessionID(sid), Window: WindowID(wid), ID: PaneID(pid),
			Dead: f[10] == "1", DeadStatus: NoStatus, Width: w, Height: h,
		},
		WindowIndex: widx, WindowName: f[20], WindowActive: f[5] == "1", AutoRename: f[6] == "1",
		Index: pidx, Active: f[9] == "1", Command: f[21], Tags: tags,
	}
	if p.Dead && f[11] != "" { // no status on a dead pane is a signal death, never 0
		st, err := strconv.Atoi(f[11])
		if err != nil {
			return SessionInfo{}, PaneInfo{}, false
		}
		p.DeadStatus = st
	}
	return s, p, true
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
