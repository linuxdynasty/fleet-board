package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// clientsFormat reads every client on the server: its pid (the client
// identity across reads), its last key time, whether it is a control-mode
// client (the agent's own observer), and the session and pane it shows.
// Session and pane ids (never names, which are the developer's text) name
// where a key landed; a browser attach through a grouped view session shows
// the real pane, so its keys credit the real pane (02h E13).
const clientsFormat = "#{client_pid} #{client_activity} #{client_control_mode} #{session_id} #{pane_id}"

// ClientInfo is one row of list-clients: who is attached and where.
type ClientInfo struct {
	// PID is the client's process id: the identity across reads. A
	// detached and re-attached person is a new client and counts as
	// input (02h: when unsure, it counts).
	PID int
	// Activity is the client's last key time (epoch seconds from tmux).
	Activity time.Time
	// Control is whether tmux calls it a control-mode client: the
	// agent's own observer, never a person.
	Control bool
	// Session is the session id the client shows ($N).
	Session SessionID
	// Pane is the client's active pane (%N).
	Pane PaneID
}

// ReadClients returns every client on the server now, for E13 (01 E13):
// the on-demand read behind the "developer typing" check before any
// SendKeys (which reads at that moment, never stored push state) and
// behind the backup poll while the input subscription is unconfirmed. A
// reply cut short or not in the asked shape is ErrBadReply; nothing
// partial is returned. No clients is an empty read, not an error.
func (c *Client) ReadClients(ctx context.Context) ([]ClientInfo, error) {
	r, err := c.run(ctx, "list-clients -F '"+clientsFormat+"'", NoID)
	if err != nil {
		return nil, err
	}
	if r.Dropped > 0 {
		return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
	}
	return parseClients(r.Body)
}

// parseClients reads one list-clients row per line: pid, key time,
// control flag (0 or 1), session id and pane id, split by single spaces.
// Anything else fails the whole read: a half-read client list would miss
// a key or credit a wrong pane.
func parseClients(body []string) ([]ClientInfo, error) {
	out := make([]ClientInfo, 0, len(body))
	for _, line := range body {
		f := strings.Split(line, " ")
		if len(f) != 5 {
			return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
		}
		sec, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || sec < 0 {
			return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
		}
		var control bool
		switch f[2] {
		case "0":
		case "1":
			control = true
		default:
			return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
		}
		session, ok := parseID([]byte(f[3]), '$')
		if !ok {
			return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
		}
		pane, ok := parseID([]byte(f[4]), '%')
		if !ok {
			return nil, fmt.Errorf("tmux: clients: %w", ErrBadReply)
		}
		out = append(out, ClientInfo{
			PID: pid, Activity: time.Unix(sec, 0),
			Control: control, Session: SessionID(session), Pane: PaneID(pane),
		})
	}
	return out, nil
}
