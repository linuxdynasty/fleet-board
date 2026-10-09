//go:build darwin || linux

package wakenet

import (
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
// it waits in Read, so Run returns when its context ends.
func TestPushSocketCloseUnblocksRead(t *testing.T) {
	t.Parallel()
	s, err := openPush()
	if err != nil {
		t.Fatalf("openPush: %v", err)
	}
	reading := make(chan struct{})
	type result struct {
		err   error
		reads int
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 1<<16)
		close(reading)
		for n := 0; ; n++ {
			if _, err := s.Read(buf); err != nil {
				done <- result{err, n}
				return
			}
		}
	}()
	<-reading
	// A real socket gives no signal that Read is parked; this lets it get there.
	time.Sleep(100 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case r := <-done:
		t.Logf("Read returned after Close (%d reads with data before): %v", r.reads, r.err)
	case <-time.After(waitLimit):
		t.Fatalf("Read still blocked %v after Close", waitLimit)
	}
}
