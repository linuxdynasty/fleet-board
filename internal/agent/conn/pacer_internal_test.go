package conn

import (
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// The pacer holds every rule that moves the failure count and the next
// Hello's reason, for both of the loop's pauses: a failure doubles the
// next wait's ceiling, and a connect, a wake or a network change resets it
// (defaults.md "Reconnect backoff").
func TestThePacerResetsOnAConnectAndOnAnEvent(t *testing.T) {
	t.Parallel()
	b := Backoff{First: time.Second, Max: time.Hour}.withDefaults()
	wake := agentv1.ConnectReason_CONNECT_REASON_WAKE
	reconnect := agentv1.ConnectReason_CONNECT_REASON_RECONNECT
	p := newPacer(b)
	if p.reason != agentv1.ConnectReason_CONNECT_REASON_START || p.failures != 0 {
		t.Fatalf("a new pacer = %+v, want START and no failures", p)
	}
	for n := 1; n <= 3; n++ {
		if w := p.onFailure(); w < b.First || w > b.First<<n {
			t.Errorf("wait after %d failures = %v, want within [%v, %v]", n, w, b.First, b.First<<n)
		}
	}
	if p.failures != 3 || p.reason != reconnect {
		t.Fatalf("after three failures = %+v, want 3 and RECONNECT", p)
	}

	p.onEvent(wake)
	if p.failures != 0 || p.reason != wake {
		t.Fatalf("after a wake = %+v, want 0 failures and WAKE", p)
	}
	for range 200 {
		q := *p
		if w := q.onFailure(); w > 2*b.First {
			t.Fatalf("the first wait after a wake = %v, want at most %v", w, 2*b.First)
		}
	}
	p.onFailure()
	if p.failures != 1 || p.reason != reconnect {
		t.Errorf("a failure after the wake = %+v, want 1 and RECONNECT", p)
	}

	p.onFailure()
	p.onConnect()
	if p.failures != 0 {
		t.Errorf("after a connect = %+v, want 0 failures", p)
	}
}
