package conn_test

import (
	"net/netip"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
)

// slowBackoff waits an hour after a failure, so a quick reconnect can only
// come from a wake or network change.
func slowBackoff(o *conn.LoopOptions) {
	o.Backoff.First, o.Backoff.Max = time.Hour, time.Hour
}

var (
	loopback = netip.MustParsePrefix("127.0.0.1/8")
	otherNet = netip.MustParsePrefix("10.9.8.7/24")
	wake     = wakenet.Event{
		Reason: agentv1.ConnectReason_CONNECT_REASON_WAKE,
		Wake:   wakenet.WakeInfo{AsleepGrew: true, AsleepDelta: time.Minute},
	}
)

func netChange(changes []wakenet.Change, prev, cur map[string][]netip.Prefix) wakenet.Event {
	return wakenet.Event{Reason: agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE, Net: wakenet.NetInfo{
		Source: wakenet.SourcePush, Changes: changes,
		Previous: wakenet.Reading{Interfaces: prev}, Current: wakenet.Reading{Interfaces: cur},
	}}
}

func TestAWakeOrANetworkChangeOnTheConnectionReconnects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   wakenet.Event
		// want is the next Hello's reason; unspecified means no reconnect.
		want agentv1.ConnectReason
		log  string
	}{
		{name: "wake", ev: wake, want: agentv1.ConnectReason_CONNECT_REASON_WAKE},
		{
			name: "default route changed",
			ev: netChange([]wakenet.Change{{Kind: wakenet.DefaultRouteChanged, Iface: "en1"}},
				map[string][]netip.Prefix{"lo0": {loopback}}, map[string][]netip.Prefix{"lo0": {loopback}}),
			want: agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE, log: "netchange.acted",
		},
		{
			name: "the connection's local address is gone",
			ev: netChange([]wakenet.Change{{Kind: wakenet.AddressRemoved, Iface: "lo0", Addr: loopback}},
				map[string][]netip.Prefix{"lo0": {loopback}}, map[string][]netip.Prefix{"lo0": nil}),
			want: agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE, log: "netchange.acted",
		},
		{
			name: "another interface changed",
			ev: netChange([]wakenet.Change{{Kind: wakenet.InterfaceAdded, Iface: "en5"}},
				map[string][]netip.Prefix{"lo0": {loopback}},
				map[string][]netip.Prefix{"lo0": {loopback}, "en5": {otherNet}}),
			log: "netchange.ignored",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			events := make(chan wakenet.Event)
			startLoop(t, a, func(o *conn.LoopOptions) {
				slowBackoff(o)
				o.Events = events
			})
			h.connected(t, 1)
			events <- tc.ev
			if tc.want == agentv1.ConnectReason_CONNECT_REASON_UNSPECIFIED {
				time.Sleep(200 * time.Millisecond)
				if h.count() != 1 || h.stream(1).ended {
					t.Error("a change that did not touch the connection closed it")
				}
			} else {
				h.connected(t, 2)
				if got := h.stream(2).hello.GetReason(); got != tc.want {
					t.Errorf("the next Hello says %v, want %v", got, tc.want)
				}
			}
			if tc.log != "" && len(a.events(t, tc.log)) != 1 {
				t.Errorf("want one %s line, got %v", tc.log, a.events(t, "netchange."))
			}
		})
	}
}

// While not connected, a wake or any network change cuts the hour-long
// backoff (ADR 0029), but two attempts stay MinGap apart.
func TestAWakeCutsTheBackoffButKeepsTheGap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   wakenet.Event
	}{
		{"wake", wake},
		{"network change", netChange([]wakenet.Change{{Kind: wakenet.InterfaceAdded, Iface: "en5"}},
			map[string][]netip.Prefix{"lo0": {loopback}},
			map[string][]netip.Prefix{"lo0": {loopback}, "en5": {otherNet}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			removeState(t, a, conn.HubAddressFile)
			// The first search takes time; the next one returns at once.
			// Browse starts mark the beginning of each whole attempt here.
			browseStarts := make(chan time.Time, 2)
			d := &fakeDiscover{hold: true}
			d.onBrowse = func() {
				browseStarts <- time.Now()
				d.mu.Lock()
				d.hold = false
				d.mu.Unlock()
			}
			h.set(func(h *connectHub) {
				h.onHello = func(n int, _ *agentv1.Hello) *agentv1.Bye {
					if n == 1 {
						return bye(agentv1.ByeReason_BYE_REASON_HUB_RESTART)
					}
					return nil
				}
			})
			events := make(chan wakenet.Event)
			const gap = 300 * time.Millisecond
			r := startLoop(t, a, func(o *conn.LoopOptions) {
				slowBackoff(o)
				o.Backoff.MinGap = gap
				o.Events = events
				useMDNS(o, d)
				o.BrowseTimeout = 250 * time.Millisecond
				o.Identity.HubAddresses = []string{h.addr}
			})
			waitFor(t, 5*time.Second, "the first Hello", func() bool { return h.count() == 1 && h.stream(1).ended })
			waitFor(t, 5*time.Second, "backoff", func() bool { return r.loop.Status().State == conn.StateBackoff })
			events <- tc.ev
			h.connected(t, 2)
			if got := h.stream(2).hello.GetReason(); got != tc.ev.Reason {
				t.Errorf("the Hello after a %s in backoff says %v, want %v", tc.name, got, tc.ev.Reason)
			}
			if got := d.count(); got != 2 {
				t.Fatalf("browses = %d, want one per attempt", got)
			}
			first, second := <-browseStarts, <-browseStarts
			const startSkew = 20 * time.Millisecond
			if d := second.Sub(first); d < gap-startSkew {
				t.Errorf("two whole attempts %v apart, want at least %v", d, gap)
			}
		})
	}
}

func logTime(t *testing.T, line map[string]any) time.Time {
	t.Helper()
	s, _ := line["time"].(string)
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("log time %q: %v", s, err)
	}
	return at
}

func TestADisplacedStreamBacksOffAndTriesAgain(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	first, second := enrolled(t, h), enrolled(t, h)
	// Longer than the second agent's connect (a few agent.db writes, each
	// up to the store's 250 ms batch window), so it is connected when the
	// first comes back.
	const wait = 2 * time.Second
	ra := startLoop(t, first, func(o *conn.LoopOptions) {
		o.BootID = "boot-a"
		o.Backoff.First, o.Backoff.Max = wait, wait
	})
	h.connected(t, 1)
	rb := startLoop(t, second, func(o *conn.LoopOptions) {
		o.BootID = "boot-b"
		slowBackoff(o)
	})
	h.connected(t, 2)
	h.connected(t, 3)
	if got := h.stream(3).hello; got.GetBootId() != "boot-a" ||
		got.GetReason() != agentv1.ConnectReason_CONNECT_REASON_RECONNECT {
		t.Errorf("stream 3 Hello = %v, want boot-a reconnecting", got)
	}
	if d := h.stream(3).at.Sub(h.stream(2).at); d < wait {
		t.Errorf("the displaced agent was back after %v, want its backoff of at least %v", d, wait)
	}
	for _, ag := range []*agent{first, second} {
		byes := ag.events(t, "connect.bye")
		if len(byes) != 1 || byes[0]["reason"] != "DUPLICATE_IDENTITY" {
			t.Errorf("connect.bye = %v", byes)
		}
	}
	ra.running(t)
	rb.running(t)
}
