//go:build darwin || linux

package wakenet

import (
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The system sources only read: the clocks, the interface list, the routing
// table, and a push socket that is opened and closed again.

func TestSystemAsleepReads(t *testing.T) {
	t.Parallel()
	d, err := SystemAsleep()
	if err != nil {
		t.Fatalf("SystemAsleep: %v", err)
	}
	if d < -asleepJitter {
		t.Errorf("time asleep since boot = %v, below the jitter allowance", d)
	}
}

func TestSystemReaderReads(t *testing.T) {
	t.Parallel()
	r, err := SystemReader().Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(r.Interfaces) == 0 {
		t.Fatal("no interface is up and running; loopback at least should be")
	}
	for _, rt := range []Route{r.DefaultV4, r.DefaultV6} {
		if rt.Iface == "" && rt.Gateway.IsValid() {
			t.Errorf("default route %+v has a gateway but no interface name", rt)
		}
	}
	again, err := SystemReader().Read()
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if ch := Diff(r, again); len(ch) != 0 {
		t.Logf("network changed between two reads (possible on a busy host): %+v", ch)
	}
}

func TestPushSocketOpens(t *testing.T) {
	t.Parallel()
	s, err := openPush()
	if err != nil {
		t.Fatalf("openPush: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestPushSocketCloseUnblocksRead: the push reader stops on Close even while
// it waits in Read, so Run returns when its context ends. A Read that Close
// can wake waits in Go's network poller ("IO wait"); a blocking one sits in a
// system call that only a routing message ends, so the test first waits for
// the reader to show as parked in the poller, then closes.
func TestPushSocketCloseUnblocksRead(t *testing.T) {
	t.Parallel()
	s, err := openPush()
	if err != nil {
		t.Fatalf("openPush: %v", err)
	}
	var reads atomic.Int64
	done := make(chan error, 1)
	go func() { done <- readUntilError(s, &reads) }()

	deadline := time.Now().Add(waitLimit)
	for !parkedInPoller(".readUntilError(") {
		if time.Now().After(deadline) {
			_ = s.Close()
			t.Fatalf("Read never waited in the network poller within %v: Close cannot wake it", waitLimit)
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	afterClose := reads.Load()
	select {
	case err := <-done:
		// One read may have taken a message just before Close and counted it
		// just after.
		if grew := reads.Load() - afterClose; grew > 1 {
			t.Fatalf("Read returned data %d times after Close returned", grew)
		}
		t.Logf("Read returned after Close (%d reads with data): %v", reads.Load(), err)
	case <-time.After(waitLimit):
		t.Fatalf("Read still blocked %v after Close", waitLimit)
	}
}

// readUntilError reads r until a read fails, counting the reads that return
// data, and returns the error.
func readUntilError(r io.Reader, reads *atomic.Int64) error {
	buf := make([]byte, 1<<16)
	for {
		if _, err := r.Read(buf); err != nil {
			return err
		}
		reads.Add(1)
	}
}

// parkedInPoller reports whether the goroutine whose stack holds fn is waiting
// in the network poller, where Close wakes it.
func parkedInPoller(fn string) bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, fn) {
			header, _, _ := strings.Cut(g, "\n")
			return strings.Contains(header, "[IO wait")
		}
	}
	return false
}
