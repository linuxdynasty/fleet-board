package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// windowActivityFormat lists every window on the server with its activity
// time (epoch seconds of its latest output). list-windows -a covers every
// session, so one command reads the whole server.
const windowActivityFormat = "#{window_id} #{window_activity}"

// ReadWindowActivity returns each window's latest activity time, for E4
// (01 E4). A reply cut short or not in the asked shape is ErrBadReply;
// nothing partial is returned.
func (c *Client) ReadWindowActivity(ctx context.Context) (map[WindowID]time.Time, error) {
	r, err := c.run(ctx, "list-windows -a -F '"+windowActivityFormat+"'", NoID)
	if err != nil {
		return nil, err
	}
	if r.Dropped > 0 {
		return nil, fmt.Errorf("tmux: window activity: %w", ErrBadReply)
	}
	out := make(map[WindowID]time.Time, len(r.Body))
	for _, line := range r.Body {
		id, at, ok := parseWindowActivity(line)
		if !ok {
			return nil, fmt.Errorf("tmux: window activity: %w", ErrBadReply)
		}
		out[id] = at
	}
	return out, nil
}

func parseWindowActivity(line string) (WindowID, time.Time, bool) {
	id, epoch, ok := strings.Cut(line, " ")
	if !ok || !strings.HasPrefix(id, "@") {
		return NoID, time.Time{}, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "@"))
	if err != nil || n < 0 {
		return NoID, time.Time{}, false
	}
	sec, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil || sec < 0 {
		return NoID, time.Time{}, false
	}
	return WindowID(n), time.Unix(sec, 0), true
}
