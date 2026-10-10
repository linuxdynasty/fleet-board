package conn_test

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
)

// With no cached or configured address, the loop browses, tries an answer
// as soon as it arrives (the browse here would run a minute), connects to
// it, and caches its name, never the address it dialed.
func TestAnMDNSAnswerIsTriedAsItArrives(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	a := enrolled(t, h)
	removeState(t, a, conn.HubAddressFile)
	d := &fakeDiscover{services: []conn.Service{service(t, h.addr, "hub.test")}, hold: true}
	r := startLoop(t, a, func(o *conn.LoopOptions) { useMDNS(o, d); o.BrowseTimeout = time.Minute })
	h.connected(t, 1)
	r.connected(t)

	want := mdnsAddr(t, h, "hub.test")
	if got := attempts(t, a); !slices.Equal(got, [][2]string{{"mdns", want}}) {
		t.Errorf("attempts = %v, want one mdns attempt at %s", got, want)
	}
	if st := r.loop.Status(); st.Peer != want {
		t.Errorf("status peer = %q, want %q", st.Peer, want)
	}
	waitFor(t, 5*time.Second, "the found name to be cached", func() bool {
		cached, err := conn.LoadHubAddress(a.dir)
		return err == nil && cached == want
	})
	waitFor(t, 5*time.Second, "the metrics", func() bool {
		m := scrape(t, a)
		return metric(m, "fleet_agent_connect_attempts_total", `result="connected"`, `source="mdns"`) == "1" &&
			metric(m, "fleet_agent_discovery_seconds_count", `result="found"`, `source="mdns"`) == "1"
	})
	// The attempt's line belongs to the agent.discover span, as does the
	// browse it came from.
	at := a.events(t, "connect.attempt")
	if at[0]["dial"] != h.addr {
		t.Errorf("connect.attempt dial = %v, want the answer's address %s", at[0]["dial"], h.addr)
	}
	if at[0]["trace_id"] == nil {
		t.Error("connect.attempt carries no trace_id: it is not logged inside agent.discover")
	}
}

// Only answers named *._fleetd._tcp.local. with a port and a unicast
// address are tried, each once; the rest are dropped without a dial. Every
// one here points at the real hub, so a stray that slipped through would
// connect first. An impostor with the right name but another CA fails the
// pin, receives nothing and the next answer is tried.
func TestStrayMDNSAnswersAreDropped(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	a := enrolled(t, h)
	removeState(t, a, conn.HubAddressFile)
	other := newCA(t)
	imp := startImpostor(t, serverCert(t, other, []string{"hub.test", "127.0.0.1"}, other.Cert.Raw))

	real := service(t, h.addr, "hub.test")
	stray := func(fn func(s *conn.Service)) conn.Service {
		s := real
		fn(&s)
		return s
	}
	d := &fakeDiscover{services: []conn.Service{
		stray(func(s *conn.Service) { s.Name = "printer._ipp._tcp.local." }),
		stray(func(s *conn.Service) { s.Name = "fleetd._fleetd._tcp.example." }),
		stray(func(s *conn.Service) { s.Name = "._fleetd._tcp.local." }),
		stray(func(s *conn.Service) { s.Port = 0 }),
		stray(func(s *conn.Service) { s.Port = 70000 }),
		stray(func(s *conn.Service) { s.AddrV4 = netip.Addr{} }),
		stray(func(s *conn.Service) { s.AddrV4 = netip.MustParseAddr("224.0.0.251") }),
		stray(func(s *conn.Service) { s.AddrV4 = netip.IPv4Unspecified() }),
		stray(func(s *conn.Service) { s.Host = "bad host!." }),
		stray(func(s *conn.Service) { s.Host = "" }),
		service(t, imp.addr, "hub.test"),
		real,
		real, // the same answer twice is tried once
	}}
	r := startLoop(t, a, func(o *conn.LoopOptions) { useMDNS(o, d) })
	h.connected(t, 1)
	r.connected(t)

	want := [][2]string{{"mdns", "hub.test:" + portOf(t, imp.addr)}, {"mdns", mdnsAddr(t, h, "hub.test")}}
	if got := attempts(t, a); !slices.Equal(got, want) {
		t.Errorf("attempts = %v, want only the impostor, then the hub: %v", got, want)
	}
	if n := imp.received.Load(); n != 0 {
		t.Errorf("the impostor received %d bytes", n)
	}
	waitFor(t, 5*time.Second, "the browse line", func() bool { return len(a.events(t, "connect.browse")) == 1 })
	b := a.events(t, "connect.browse")[0]
	if b["kept"] != float64(2) || b["dropped"] != float64(10) {
		t.Errorf("connect.browse = %v, want kept 2 (one duplicate not counted) and dropped 10", b)
	}
	by, _ := b["dropped_by"].(map[string]any)
	if by["not_hub_name"] != float64(3) || by["bad_port"] != float64(2) || by["bad_addr"] != float64(3) ||
		by["bad_host"] != float64(2) || len(by) != 4 {
		t.Errorf("connect.browse dropped_by = %v", by)
	}
	if n := metric(scrape(t, a), "fleet_agent_connect_attempts_total", `result="untrusted"`, `source="mdns"`); n != "1" {
		t.Errorf("untrusted mdns attempts = %q, want 1", n)
	}
	if st := r.loop.Status().Browse; st.Kept != 2 || st.Dropped != 10 || st.Time.IsZero() || st.ErrKind != "" {
		t.Errorf("status browse = %+v", st)
	}
}

// The browse comes only after the cached address failed, and a browse
// that finds nothing moves on to the configured addresses in the same
// attempt (ADR 0028).
func TestABrowseThatFindsNothingFallsThroughToConfigured(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	dead := closedAddr(t)
	writeState(t, a, conn.HubAddressFile, dead+"\n")
	var before int
	d := &fakeDiscover{}
	d.onBrowse = func() { before = len(a.events(t, "connect.attempt")) }
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		useMDNS(o, d)
		o.Identity.HubAddresses = []string{h.addr}
	})
	h.connected(t, 1)
	r.connected(t)

	if got, want := attempts(t, a), [][2]string{{"cached", dead}, {"configured", h.addr}}; !slices.Equal(got, want) {
		t.Errorf("attempts = %v, want %v", got, want)
	}
	if d.count() != 1 || before != 1 {
		t.Errorf("browses = %d with %d attempts before it, want 1 after the cached one", d.count(), before)
	}
	if st := r.loop.Status().Browse; st.Kept != 0 || st.Time.IsZero() {
		t.Errorf("status browse = %+v, want a browse that kept nothing", st)
	}
}

// A cached address that connects needs no browse.
func TestTheCachedAddressNeedsNoBrowse(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	d := &fakeDiscover{}
	r := startLoop(t, a, func(o *conn.LoopOptions) { useMDNS(o, d) })
	h.connected(t, 1)
	r.connected(t)
	if d.count() != 0 {
		t.Errorf("browses = %d, want 0", d.count())
	}
}

// A browse that fails (no multicast interface) is logged and kept in the
// status, and the configured address still connects.
func TestAFailedBrowseFallsThrough(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	removeState(t, a, conn.HubAddressFile)
	d := &fakeDiscover{err: errors.New("failed to bind to any multicast udp port")}
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		useMDNS(o, d)
		o.Identity.HubAddresses = []string{h.addr}
	})
	h.connected(t, 1)
	r.connected(t)
	if st := r.loop.Status().Browse; st.ErrKind != "io" || st.Time.IsZero() {
		t.Errorf("status browse = %+v, want err_kind io", st)
	}
	b := a.events(t, "connect.browse")
	if len(b) != 1 || b[0]["err_kind"] != "io" || b[0]["level"] != "warn" {
		t.Errorf("connect.browse = %v, want one warning with err_kind io", b)
	}
}

// One browse per attempt, on the backoff: with nothing reachable, every
// attempt browses once after its cached address failed, and a wake in a
// long backoff starts the next attempt, and its browse, at once.
func TestOneBrowsePerAttemptOnTheBackoff(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	writeState(t, a, conn.HubAddressFile, closedAddr(t)+"\n")
	d := &fakeDiscover{}
	r := startLoop(t, a, func(o *conn.LoopOptions) { useMDNS(o, d) })
	waitFor(t, 10*time.Second, "five failed attempts", func() bool { return len(a.events(t, "connect.backoff")) >= 5 })
	r.cancel()
	_ = r.result(t, 10*time.Second)
	// The stop may land between an attempt's cached try and its browse,
	// so the last attempt may have none.
	if tries := len(a.events(t, "connect.attempt")); d.count() != tries && d.count() != tries-1 {
		t.Errorf("browses = %d, attempts = %d; want one browse per attempt", d.count(), tries)
	}
}

// After a wake ends a long backoff, the new attempt browses at once.
func TestAWakeStartsTheNextBrowse(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	writeState(t, a, conn.HubAddressFile, closedAddr(t)+"\n")
	d := &fakeDiscover{}
	events := make(chan wakenet.Event, 1)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		useMDNS(o, d)
		o.Events = events
		slowBackoff(o)
	})
	waitFor(t, 10*time.Second, "the first browse and its backoff", func() bool {
		return d.count() == 1 && len(a.events(t, "connect.backoff")) == 1
	})
	events <- wake
	waitFor(t, 10*time.Second, "the wake's browse", func() bool { return d.count() == 2 })
	r.running(t)
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(int(ap.Port()))
}
