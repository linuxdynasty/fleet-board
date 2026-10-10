package conn

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// Which answer addresses are dialed: unicast addresses of other machines
// only. An IPv4 address inside IPv6 is judged as IPv4, so ::ffff:0.0.0.0
// (which dials this machine on Linux) is refused like 0.0.0.0.
func TestOnlyAnotherMachinesUnicastAddressIsDialed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v4, v6 string
		dial   string // empty: dropped as bad_addr
	}{
		{v4: "192.168.1.5", dial: "192.168.1.5:7443"},
		{v6: "2001:db8::1", dial: "[2001:db8::1]:7443"},
		{v6: "fe80::1%en0", dial: "[fe80::1%en0]:7443"},
		{v6: "fe80::1"},
		{v4: "127.0.0.1"},
		{v6: "::1"},
		{v4: "0.0.0.0"},
		{v6: "::"},
		{v6: "::ffff:0.0.0.0"},
		{v6: "::ffff:127.0.0.1"},
		{v6: "::ffff:255.255.255.255"},
		{v6: "::ffff:224.0.0.251"},
		{v4: "255.255.255.255"},
		{v4: "224.0.0.251"},
		{v6: "ff02::fb"},
		{v6: "::ffff:192.168.1.5", dial: "192.168.1.5:7443"},
		{v4: "127.0.0.1", v6: "2001:db8::1", dial: "[2001:db8::1]:7443"},
		{},
	}
	for _, tc := range cases {
		t.Run(tc.v4+"|"+tc.v6, func(t *testing.T) {
			t.Parallel()
			s := Service{Name: "fleetd-a._fleetd._tcp.local.", Host: "hub.local.", Port: 7443}
			if tc.v4 != "" {
				s.AddrV4 = netip.MustParseAddr(tc.v4)
			}
			if tc.v6 != "" {
				s.AddrV6 = netip.MustParseAddr(tc.v6)
			}
			c, why := candidateOf(s, false)
			switch {
			case tc.dial == "" && why != dropBadAddr:
				t.Errorf("kept %+v (%q), want it dropped as bad_addr", c, why)
			case tc.dial != "" && (why != "" || c.dial != tc.dial || c.addr != "hub.local:7443"):
				t.Errorf("candidate %+v (%q), want dial %s", c, why, tc.dial)
			}
		})
	}
}

// Every drop has its reason; a target naming this machine is a bad host,
// so a hostile answer cannot have the agent cache localhost as its hub.
func TestEachDroppedAnswerHasItsReason(t *testing.T) {
	t.Parallel()
	ok := Service{Name: "fleetd-a._fleetd._tcp.local.", Host: "Hub.Local.", Port: 7443, AddrV4: netip.MustParseAddr("192.168.1.5")}
	with := func(fn func(s *Service)) Service {
		s := ok
		fn(&s)
		return s
	}
	cases := []struct {
		s   Service
		why string
	}{
		{ok, ""},
		{with(func(s *Service) { s.Name = "printer._ipp._tcp.local." }), dropNotHubName},
		{with(func(s *Service) { s.Name = "._fleetd._tcp.local." }), dropNotHubName},
		{with(func(s *Service) { s.Port = 0 }), dropBadPort},
		{with(func(s *Service) { s.Host = "" }), dropBadHost},
		{with(func(s *Service) { s.Host = "localhost." }), dropBadHost},
		{with(func(s *Service) { s.Host = "x.localhost." }), dropBadHost},
		{with(func(s *Service) { s.Host = "127.0.0.1" }), dropBadHost},
		{with(func(s *Service) { s.Host = "bad host!." }), dropBadHost},
		{with(func(s *Service) { s.AddrV4 = netip.Addr{} }), dropBadAddr},
	}
	for _, tc := range cases {
		c, why := candidateOf(tc.s, false)
		if why != tc.why {
			t.Errorf("%+v: dropped as %q, want %q", tc.s, why, tc.why)
		}
		if why == "" && c.addr != "hub.local:7443" {
			t.Errorf("the name is %q, want it in lower case", c.addr)
		}
	}
}

type answers []Service

func (a answers) Browse(_ context.Context, _ time.Duration, found func(Service)) error {
	for _, s := range a {
		found(s)
	}
	return nil
}

// At most 16 answers become candidates; the rest are dropped as over_cap.
func TestABrowseKeepsSixteenAnswers(t *testing.T) {
	t.Parallel()
	var a answers
	for i := range 20 {
		a = append(a, Service{
			Name: fmt.Sprintf("f%d._fleetd._tcp.local.", i), Host: fmt.Sprintf("h%d.local.", i),
			Port: 7443, AddrV4: netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)}),
		})
	}
	var st BrowseStatus
	b := startBrowse(context.Background(), a, time.Second, mdnsLimits{}, func(s BrowseStatus, _ time.Duration) { st = s })
	n := 0
	for range b.cands {
		n++
	}
	b.stop()
	if n != 16 || st.Kept != 16 || st.Dropped != 4 || st.DroppedBy[dropOverCap] != 4 {
		t.Errorf("%d candidates, status %+v; want 16 kept and 4 dropped over the cap", n, st)
	}
}

// The library keeps writing an answer it handed over until its query
// returns. The browser reads an answer only after that: this querier
// rewrites every field after handing the entry over and leaves its final
// values just before it returns, so a read inside the query sees other
// values (and -race flags it).
func TestAnAnswerIsReadOnlyAfterItsQueryReturned(t *testing.T) {
	t.Parallel()
	final := Service{
		Name: "fleetd-a._fleetd._tcp.local.", Host: "final.local.", Port: 7443,
		AddrV4: netip.MustParseAddr("192.168.1.9"), AddrV6: netip.MustParseAddr("fe80::9%en0"),
	}
	q := func(_ context.Context, p *mdns.QueryParam) error {
		e := &mdns.ServiceEntry{Name: final.Name, Host: "first.local.", Port: 1, AddrV4: net.IPv4(10, 0, 0, 1)}
		p.Entries <- e
		for i := range 2000 {
			e.Host, e.Port = fmt.Sprintf("h%d.local.", i), i+2
			e.AddrV4 = net.IPv4(10, 0, byte(i>>8), byte(i))
		}
		e.Host, e.Port, e.AddrV4 = final.Host, final.Port, net.IPv4(192, 168, 1, 9)
		e.AddrV6IPAddr = &net.IPAddr{IP: net.ParseIP("fe80::9"), Zone: "en0"}
		time.Sleep(time.Millisecond) // the window ends
		return nil
	}
	m := mdnsBrowser{query: q, window: 10 * time.Millisecond}
	var got []Service
	if err := m.Browse(context.Background(), 35*time.Millisecond, func(s Service) { got = append(got, s) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != final {
		t.Errorf("the browse handed over %+v, want only %+v (the same answer in later windows once)", got, final)
	}
}

// Answers are drained after a query that ends with an error, the error is
// returned, and an IPv4 address the library gives as IPv6 is IPv4.
func TestABrowseDrainsAFailedQuery(t *testing.T) {
	t.Parallel()
	q := func(_ context.Context, p *mdns.QueryParam) error {
		p.Entries <- &mdns.ServiceEntry{
			Name: "a._fleetd._tcp.local.", Host: "a.local.", Port: 7443,
			AddrV6IPAddr: &net.IPAddr{IP: net.ParseIP("::ffff:192.168.1.7"), Zone: "en0"},
		}
		return fmt.Errorf("write udp6: sendto: no route to host")
	}
	m := mdnsBrowser{query: q, window: time.Second}
	var got []Service
	err := m.Browse(context.Background(), time.Second, func(s Service) { got = append(got, s) })
	if err == nil || len(got) != 1 || got[0].AddrV6 != netip.MustParseAddr("192.168.1.7") {
		t.Errorf("Browse = %v with %+v, want the error and the answer with 192.168.1.7", err, got)
	}
}

// On macOS the browse asks over IPv4 only (spike 0.7 finding 3); every
// window asks for _fleetd._tcp in local within what is left of the browse.
func TestTheQueryParameters(t *testing.T) {
	t.Parallel()
	var params []mdns.QueryParam
	q := func(_ context.Context, p *mdns.QueryParam) error {
		params = append(params, *p)
		time.Sleep(p.Timeout)
		return nil
	}
	m := newMDNS()
	m.query, m.window = q, 20*time.Millisecond
	if err := m.Browse(context.Background(), 50*time.Millisecond, func(Service) {}); err != nil {
		t.Fatal(err)
	}
	if len(params) < 2 {
		t.Fatalf("%d queries in 50 ms of 20 ms windows", len(params))
	}
	for _, p := range params {
		if p.Service != "_fleetd._tcp" || p.Domain != "local" || p.Timeout > 20*time.Millisecond || p.Logger == nil {
			t.Errorf("query %+v", p)
		}
		if want := runtime.GOOS == "darwin"; p.DisableIPv6 != want {
			t.Errorf("DisableIPv6 = %v, want %v on this system", p.DisableIPv6, want)
		}
	}
}
