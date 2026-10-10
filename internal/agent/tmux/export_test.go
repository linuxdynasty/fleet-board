package tmux

import (
	"errors"
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

// SetBackoffForTest replaces the wait between attach tries. Set it before Run.
func SetBackoffForTest(c *Client, f func(n int) time.Duration) { c.backoff = f }

// SetWriteTimeoutForTest replaces the bound on one command write. Set it
// before Run.
func SetWriteTimeoutForTest(c *Client, d time.Duration) { c.writeTimeout = d }

// BackoffForTest is the production wait after n failed tries in a row.
func BackoffForTest(n int) time.Duration { return backoff(n) }

// PickSessionForTest runs the pick rule over list-sessions output.
func PickSessionForTest(list string) (SessionID, bool, error) { return pickSession([]byte(list)) }

// IsAbsentForTest reports whether err means no server or no session.
func IsAbsentForTest(err error) bool { return errors.Is(err, errAbsent) }
