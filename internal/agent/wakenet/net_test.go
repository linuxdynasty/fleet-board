package wakenet

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeSocket is a push socket the test feeds one read at a time.
type fakeSocket struct {
	reads  chan []byte
	errs   chan error
	closed chan struct{}
	once   sync.Once
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{reads: make(chan []byte), errs: make(chan error), closed: make(chan struct{})}
}

func (s *fakeSocket) Read(p []byte) (int, error) {
	select {
	case b := <-s.reads:
		return copy(p, b), nil
	case err := <-s.errs:
		return 0, err
	case <-s.closed:
		return 0, os.ErrClosed
	}
}

func (s *fakeSocket) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// fakeReader returns whatever reading the test set last.
type fakeReader struct {
	mu    sync.Mutex
	r     Reading
	err   error
	calls int
}

func (f *fakeReader) set(r Reading, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.r, f.err = r, err
}

// Read returns a fresh copy each time, as the system reader does.
func (f *fakeReader) Read() (Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	r := f.r
	if f.r.Interfaces != nil {
		r.Interfaces = make(map[string][]netip.Prefix, len(f.r.Interfaces))
		for n, ps := range f.r.Interfaces {
			r.Interfaces[n] = append([]netip.Prefix(nil), ps...)
		}
	}
	return r, f.err
}

func (f *fakeReader) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// netRig is a network watcher on fakes.
type netRig struct {
	clock  *fakeClock
	sock   *fakeSocket
	reader *fakeReader
	logs   *logCapture
	out    chan Event
	net    *Net
	stop   func()
}

// newNetRig starts a watcher. openErr makes the push socket fail to open.
func newNetRig(t *testing.T, base Reading, baseErr, openErr error) *netRig {
	t.Helper()
	g := &netRig{
		clock: newFakeClock(), sock: newFakeSocket(), reader: &fakeReader{},
		logs: newLogCapture(), out: make(chan Event, 16),
	}
	g.reader.set(base, baseErr)
	g.net = &Net{
		Clock:  g.clock,
		Reader: g.reader,
		OpenPush: func() (io.ReadCloser, error) {
			if openErr != nil {
				return nil, openErr
			}
			return g.sock, nil
		},
		Hint: netlinkHint,
		Log:  g.logs.logger(),
	}
	g.stop = runLoop(t, func(ctx context.Context) { g.net.Run(ctx, g.out) })
	return g
}

// hint sends one netlink "address added" at mono time at and returns the
// debounce timer the watcher asked for.
func (g *netRig) hint(t *testing.T, at time.Duration) *fakeTimer {
	t.Helper()
	g.clock.setMono(at)
	g.sock.reads <- nlMsg(nlNewAddr, ifaddrmsg(afInet6Linux, 64, 2))
	return g.clock.nextTimer(t)
}

// settle fires tm at mono time at and waits until the watcher has compared.
func (g *netRig) settle(t *testing.T, tm *fakeTimer, at time.Duration) string {
	t.Helper()
	g.clock.setMono(at)
	tm.fire()
	return g.logs.waitFor(t, "netchange.unchanged", "netchange.detected", "netchange.read_failed", "netchange.baseline")
}

func TestDebounce(t *testing.T) {
	t.Parallel()
	const ms = time.Millisecond
	tests := []struct {
		name     string
		hints    []time.Duration
		want     []time.Duration // the wait asked after each hint
		settleAt time.Duration
	}{
		{"one hint waits 500 ms", []time.Duration{0}, []time.Duration{500 * ms}, 500 * ms},
		{
			name:     "each hint restarts the 500 ms quiet wait",
			hints:    []time.Duration{0, 300 * ms, 600 * ms},
			want:     []time.Duration{500 * ms, 500 * ms, 500 * ms},
			settleAt: 1100 * ms,
		},
		{
			name:     "never more than 2 s after the first hint",
			hints:    []time.Duration{0, 400 * ms, 800 * ms, 1200 * ms, 1600 * ms},
			want:     []time.Duration{500 * ms, 500 * ms, 500 * ms, 500 * ms, 400 * ms},
			settleAt: 2000 * ms,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newNetRig(t, homeReading(), nil, nil)
			g.logs.waitFor(t, "netchange.baseline")
			var tm *fakeTimer
			for i, at := range tc.hints {
				tm = g.hint(t, at)
				if tm.d != tc.want[i] {
					t.Fatalf("hint %d at %v: wait %v, want %v", i, at, tm.d, tc.want[i])
				}
			}
			if n := g.reader.count(); n != 1 {
				t.Fatalf("read %d times before the wait ended, want 1 (the baseline)", n)
			}
			if got := g.settle(t, tm, tc.settleAt); got != "netchange.unchanged" {
				t.Fatalf("after the wait: %s, want netchange.unchanged", got)
			}
			if n := g.reader.count(); n != 2 {
				t.Errorf("read %d times, want 2", n)
			}

			next := g.hint(t, tc.settleAt+100*ms)
			if next.d != 500*ms {
				t.Errorf("first hint after a read waits %v, want a fresh 500ms", next.d)
			}
		})
	}
}

func TestRealChangeAfterHint(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	cur := homeReading()
	delete(cur.Interfaces, "enp3s0")
	cur.DefaultV4, cur.DefaultV6 = Route{}, Route{}
	g.reader.set(cur, nil)

	if got := g.settle(t, g.hint(t, 0), 500*time.Millisecond); got != "netchange.detected" {
		t.Fatalf("got %s, want netchange.detected", got)
	}
	ev := <-g.out
	if ev.Reason != reasonNet || ev.Net.Source != SourcePush {
		t.Fatalf("event %v from %s, want NETWORK_CHANGE from push", ev.Reason, ev.Net.Source)
	}
	if len(ev.Net.Changes) != 3 {
		t.Errorf("changes = %+v, want interface removed and both default routes", ev.Net.Changes)
	}
	if d := Decide(ev, Conn{}); !d.Reconnect || d.Why != WhyNotConnected {
		t.Errorf("not connected: %+v, want a reset and an attempt", d)
	}
}

func TestEventReadingIsTheCallersCopy(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	cur := homeReading()
	cur.Interfaces["tun0"] = []netip.Prefix{pfx("10.8.0.2/24")}
	g.reader.set(cur, nil)
	g.settle(t, g.hint(t, 0), 500*time.Millisecond)
	ev := <-g.out
	clear(ev.Net.Current.Interfaces)

	if got := g.settle(t, g.hint(t, time.Second), 1500*time.Millisecond); got != "netchange.unchanged" {
		t.Fatalf("after the caller edited its event: %s, want netchange.unchanged", got)
	}
}

func TestReadFailureKeepsTheBaseline(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	g.reader.set(Reading{}, errors.New("route dump: no buffer space"))
	if got := g.settle(t, g.hint(t, 0), 500*time.Millisecond); got != "netchange.read_failed" {
		t.Fatalf("got %s, want netchange.read_failed", got)
	}
	cur := homeReading()
	cur.Interfaces["tun0"] = []netip.Prefix{pfx("10.8.0.2/24")}
	g.reader.set(cur, nil)
	if got := g.settle(t, g.hint(t, time.Second), 1500*time.Millisecond); got != "netchange.detected" {
		t.Fatalf("got %s, want netchange.detected", got)
	}
	ev := <-g.out
	if len(ev.Net.Changes) != 1 || ev.Net.Changes[0] != (Change{Kind: InterfaceAdded, Iface: "tun0"}) {
		t.Errorf("changes = %+v, want tun0 added against the first baseline", ev.Net.Changes)
	}
}

func TestFailedBaselineTakesTheNextReading(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, Reading{}, errors.New("no interfaces"), nil)
	g.logs.waitFor(t, "netchange.read_failed")
	g.reader.set(homeReading(), nil)
	if got := g.settle(t, g.hint(t, 0), 500*time.Millisecond); got != "netchange.baseline" {
		t.Fatalf("got %s, want netchange.baseline", got)
	}
	if evs := drain(g.out); len(evs) != 0 {
		t.Errorf("events = %+v, want none: there was nothing to compare with", evs)
	}
}

func TestBufferOverflowIsAHint(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	g.sock.errs <- os.NewSyscallError("recvmsg", syscall.ENOBUFS)
	if tm := g.clock.nextTimer(t); tm.d != 500*time.Millisecond {
		t.Errorf("wait after ENOBUFS = %v, want 500ms", tm.d)
	}
	if g.net.PushOff() || g.logs.count("netchange.push_off") != 0 {
		t.Error("ENOBUFS turned the push socket off; it is only a hint")
	}
}

func TestPushSocketOff(t *testing.T) {
	t.Parallel()
	changed := homeReading()
	changed.DefaultV4 = Route{Iface: "wlan0", Gateway: addr("10.0.0.1")}
	tests := []struct {
		name    string
		openErr error
		die     error
	}{
		{name: "cannot be opened", openErr: errors.New("socket: address family not supported")},
		{name: "dies while running", die: errors.New("read netlink: bad file descriptor")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newNetRig(t, homeReading(), nil, tc.openErr)
			g.logs.waitFor(t, "netchange.baseline")
			if tc.die != nil {
				if g.net.PushOff() {
					t.Fatal("push socket reported off while it works")
				}
				g.sock.errs <- tc.die
			}
			poll := g.clock.nextTimer(t)
			if poll.d != 15*time.Second {
				t.Fatalf("poll wait = %v, want 15s", poll.d)
			}
			if !g.net.PushOff() {
				t.Error("PushOff() = false with the push socket off")
			}

			if got := g.settle(t, poll, 15*time.Second); got != "netchange.unchanged" {
				t.Fatalf("first poll: %s, want netchange.unchanged", got)
			}
			g.reader.set(changed, nil)
			if got := g.settle(t, g.clock.nextTimer(t), 30*time.Second); got != "netchange.detected" {
				t.Fatalf("second poll: %s, want netchange.detected", got)
			}
			ev := <-g.out
			if ev.Net.Source != SourcePoll || len(ev.Net.Changes) != 1 || ev.Net.Changes[0].Kind != DefaultRouteChanged {
				t.Errorf("event = %+v, want one default route change from the poll", ev.Net)
			}
			if got := g.settle(t, g.clock.nextTimer(t), 45*time.Second); got != "netchange.unchanged" {
				t.Fatalf("third poll: %s, want netchange.unchanged", got)
			}
			g.stop()
			if n := g.logs.count("netchange.push_off"); n != 1 {
				t.Errorf("netchange.push_off logged %d times, want exactly 1", n)
			}
		})
	}
}

func TestNoPollWhilePushWorks(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	select {
	case tm := <-g.clock.timers:
		t.Fatalf("watcher asked for a %v timer with no hint; there is no always-on poll", tm.d)
	case <-time.After(50 * time.Millisecond):
	}
}
