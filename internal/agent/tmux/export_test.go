package tmux

import "time"

// SetClockForTest replaces the clock that spaces the log lines.
func SetClockForTest(m *Metrics, now func() time.Time) { m.now = now }
