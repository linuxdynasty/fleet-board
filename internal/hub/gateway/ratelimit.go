package gateway

import (
	"sync"
	"time"
)

// HelloEvery is the hub's Hello rate limit: one Hello per machine per 5 s
// (defaults.md, "Reconnect backoff"; 03n "Flapping network").
const HelloEvery = 5 * time.Second

// helloLimit keeps when each machine's last Hello passed it. A refused
// Hello does not move the window, so an agent retrying early is never
// starved. Memory is one entry per machine that said Hello within the
// window; older entries are dropped as the map grows.
type helloLimit struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newHelloLimit() *helloLimit { return &helloLimit{last: map[string]time.Time{}} }

// sweepAt is the map size past which allow drops entries older than the
// window.
const sweepAt = 1024

// allow reports whether machineID may say Hello at now and, when not, how
// long until it may. A clock that stepped back allows the Hello and starts
// the window again rather than refusing until it catches up.
func (l *helloLimit) allow(machineID string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.last[machineID]; ok {
		if since := now.Sub(last); since >= 0 && since < HelloEvery {
			return false, HelloEvery - since
		}
	}
	if len(l.last) >= sweepAt {
		for id, at := range l.last {
			if now.Sub(at) >= HelloEvery {
				delete(l.last, id)
			}
		}
	}
	l.last[machineID] = now
	return true, 0
}
