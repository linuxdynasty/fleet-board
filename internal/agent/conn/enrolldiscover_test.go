package conn_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// enroll with no --hub browses and enrolls with the first answer whose
// handshake proves it holds the token's CA. A stray service is never
// dialed, and an impostor with the right name but another CA gets a
// handshake and nothing else: no token, no CSR.
func TestEnrollWithoutAHubFindsItByMDNS(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	other := newCA(t)
	imp := startImpostor(t, serverCert(t, other, []string{"hub.test", "127.0.0.1"}, other.Cert.Raw))
	stray := service(t, h.addr, "hub.test")
	stray.Name = "printer._ipp._tcp.local."
	d := &fakeDiscover{services: []conn.Service{stray, service(t, imp.addr, "hub.test"), service(t, h.addr, "hub.test")}}
	a := newAgent(t)
	res, err := a.enrollWith(t, "", h.token, enrollMDNS(d))
	if err != nil {
		t.Fatal(err)
	}
	want := mdnsAddr(t, h, "hub.test")
	if res.HubAddress != want || !res.Discovered {
		t.Errorf("enrolled through %q (discovered %v), want %q found by mDNS", res.HubAddress, res.Discovered, want)
	}
	if n := len(h.requests()); n != 1 {
		t.Errorf("the hub got %d Enroll requests, want 1", n)
	}
	if n := imp.received.Load(); n != 0 || imp.accepted.Load() != 1 {
		t.Errorf("the impostor got %d connections and %d bytes after the handshake, want 1 and 0", imp.accepted.Load(), n)
	}
	if cached, err := conn.LoadHubAddress(a.dir); err != nil || cached != want {
		t.Errorf("cached hub address = %q, %v; want %q", cached, err, want)
	}
	l := a.events(t, "enroll.discovered")
	if len(l) != 1 || l[0]["peer"] != want || l[0]["kept"] != float64(2) || l[0]["dropped"] != float64(1) {
		t.Errorf("enroll.discovered = %v", l)
	}
}

// enroll with no --hub that finds no hub holding the token's CA exits with
// an error naming --hub and saying what it saw, and sends nothing: no
// request reaches any hub and nothing is saved. The firewall is named only
// when nothing answered; the time cap or a stop names neither.
func TestEnrollWithoutAHubAndNoneFoundSendsNothing(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	other := newCA(t)
	imp := startImpostor(t, serverCert(t, other, []string{"hub.test", "127.0.0.1"}, other.Cert.Raw))
	hole := blackhole(t)
	stray := service(t, h.addr, "hub.test")
	stray.Name = "printer._ipp._tcp.local."
	cases := []struct {
		name     string
		services []conn.Service
		err      error
		hold     bool
		timeout  time.Duration
		why      string
		firewall bool
		errKind  string
	}{
		{name: "nothing answers", why: "no fleetd answered", firewall: true, errKind: "timeout"},
		{name: "only a stray service", services: []conn.Service{stray}, why: "no fleetd answered", firewall: true, errKind: "timeout"},
		{
			name: "only an impostor", services: []conn.Service{service(t, imp.addr, "hub.test")},
			why: "1 answered, none proved it holds this token's CA", errKind: "refused",
		},
		{
			name: "only a closed port", services: []conn.Service{service(t, closedAddr(t), "hub.test")},
			why: "1 answered but could not be reached", errKind: "refused",
		},
		{
			name: "only an answer that never replies", services: []conn.Service{service(t, hole, "hub.test")},
			why: "1 answered but could not be reached", errKind: "timeout",
		},
		{name: "an impostor and a closed port", services: []conn.Service{
			service(t, imp.addr, "hub.test"), service(t, closedAddr(t), "hub.test"),
		}, why: "2 answered: 1 could not be reached, 1 did not prove", errKind: "refused"},
		{
			name: "no multicast", err: errors.New("failed to bind to any multicast udp port"),
			why: "the mDNS browse could not run", firewall: true, errKind: "io",
		},
		{name: "the time cap", hold: true, timeout: 300 * time.Millisecond, why: "ran out of time", errKind: "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDiscover{services: tc.services, err: tc.err, hold: tc.hold}
			a := newAgent(t)
			_, err := a.enrollWith(t, "", h.token, func(o *conn.Options) {
				enrollMDNS(d)(o)
				conn.SetEnrollMDNSLimits(o, 300*time.Millisecond, 0)
				o.BrowseTimeout = 500 * time.Millisecond
				if tc.timeout > 0 {
					o.Timeout, o.BrowseTimeout = tc.timeout, time.Minute
				}
			})
			if err == nil || !strings.Contains(err.Error(), tc.why) || !strings.Contains(err.Error(), "nothing was sent") {
				t.Fatalf("enroll = %v, want %q and nothing sent", err, tc.why)
			}
			if !tc.hold && (!errors.Is(err, conn.ErrNoHubFound) || !strings.Contains(err.Error(), "--hub")) {
				t.Errorf("enroll = %v, want ErrNoHubFound naming --hub", err)
			}
			if got := strings.Contains(err.Error(), "firewall"); got != tc.firewall {
				t.Errorf("names the firewall = %v, want %v: %v", got, tc.firewall, err)
			}
			if n := len(h.requests()); n != 0 {
				t.Errorf("the hub got %d Enroll requests", n)
			}
			if n := imp.received.Load(); n != 0 {
				t.Errorf("the impostor received %d bytes", n)
			}
			if _, err := conn.LoadIdentity(a.dir); !errors.Is(err, conn.ErrNotEnrolled) {
				t.Errorf("LoadIdentity = %v, want ErrNotEnrolled", err)
			}
			f := a.events(t, "enroll.failed")
			if len(f) != 1 || f[0]["step"] != "discover" || f[0]["err_kind"] != tc.errKind {
				t.Errorf("enroll.failed = %v, want step discover, err_kind %s", f, tc.errKind)
			}
		})
	}
}

// Each answer gets its own short budget, so answers that accept and never
// reply cannot use up the enroll: the hub behind three of them is still
// found well inside the time cap.
func TestEnrollGivesEachAnswerItsOwnBudget(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	var svcs []conn.Service
	for range 3 {
		svcs = append(svcs, service(t, blackhole(t), "hub.test"))
	}
	d := &fakeDiscover{services: append(svcs, service(t, h.addr, "hub.test"))}
	a := newAgent(t)
	start := time.Now()
	res, err := a.enrollWith(t, "", h.token, func(o *conn.Options) {
		enrollMDNS(d)(o)
		conn.SetEnrollMDNSLimits(o, 200*time.Millisecond, 5*time.Second)
		o.Timeout = 5 * time.Second
	})
	if err != nil || !res.Discovered {
		t.Fatalf("enroll = %+v, %v; want the hub found behind three silent answers", res, err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("enroll took %v: the silent answers were not cut at their budget", took)
	}
}

// The search for the hub is capped as a whole: past the cap no further
// answer is tried.
func TestEnrollCapsTheSearch(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	var svcs []conn.Service
	for range 6 {
		svcs = append(svcs, service(t, blackhole(t), "hub.test"))
	}
	d := &fakeDiscover{services: append(svcs, service(t, h.addr, "hub.test"))}
	a := newAgent(t)
	_, err := a.enrollWith(t, "", h.token, func(o *conn.Options) {
		enrollMDNS(d)(o)
		conn.SetEnrollMDNSLimits(o, 200*time.Millisecond, 500*time.Millisecond)
	})
	if !errors.Is(err, conn.ErrNoHubFound) || !strings.Contains(err.Error(), "could not be reached") {
		t.Fatalf("enroll = %v, want no hub found after the cap", err)
	}
	if n := len(h.requests()); n != 0 {
		t.Errorf("the hub got %d requests past the cap", n)
	}
}

// A given --hub is used as it is, and an enrolled machine never browses.
func TestEnrollBrowsesOnlyWithoutAHubAndUnenrolled(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	d := &fakeDiscover{}
	a := newAgent(t)
	if _, err := a.enrollWith(t, h.addr, h.token, enrollMDNS(d)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.enrollWith(t, "", h.token, enrollMDNS(d)); !errors.Is(err, conn.ErrAlreadyEnrolled) {
		t.Fatalf("enroll again = %v, want ErrAlreadyEnrolled", err)
	}
	if d.count() != 0 {
		t.Errorf("browses = %d, want 0", d.count())
	}
	if got := len(h.requests()); got != 1 {
		t.Errorf("the hub got %d requests, want 1", got)
	}
	if len(a.events(t, "enroll.discovered")) != 0 {
		t.Error("enroll.discovered logged")
	}
}
