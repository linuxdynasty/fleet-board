package tmux

import (
	"io"
	"time"
)

// SetClockForTest replaces the clock that spaces the log lines.
func SetClockForTest(m *Metrics, now func() time.Time) { m.now = now }

// AttemptsForTest is how many attach tries Run has started.
func AttemptsForTest(c *Client) int64 { return c.attempts.Load() }

// SetTeeForTest copies every byte the control client reads from tmux to w,
// from the next attach on. Set it before Run.
func SetTeeForTest(c *Client, w io.Writer) { c.tee = w }
