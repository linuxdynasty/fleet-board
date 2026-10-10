package conntest

import (
	"context"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
)

// Thief binds an address in a loop, as another process on a busy machine
// might when a port is free. A test that restarts a hub on the same port
// uses it to prove the port is never free.
type Thief struct {
	stolen atomic.Int64
	done   chan struct{}
}

// StartThief starts a thief for addr. It stops when the test (or subtest)
// that started it ends, also when that test fails midway.
func StartThief(t *testing.T, addr string) *Thief {
	t.Helper()
	th := &Thief{done: make(chan struct{})}
	stop := make(chan struct{})
	go func() {
		defer close(th.done)
		var lc net.ListenConfig
		for {
			select {
			case <-stop:
				return
			default:
			}
			if ln, err := lc.Listen(context.Background(), "tcp", addr); err == nil {
				th.stolen.Add(1)
				_ = ln.Close()
			}
			runtime.Gosched()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-th.done
	})
	return th
}

// Stolen is how many times the thief bound the address.
func (th *Thief) Stolen() int64 { return th.stolen.Load() }

// Done is closed once the thief's goroutine has returned.
func (th *Thief) Done() <-chan struct{} { return th.done }
