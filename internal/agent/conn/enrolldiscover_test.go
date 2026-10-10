package conn_test

import (
	"errors"
	"strings"
	"testing"

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
	res, err := a.enrollWith(t, "", h.token, func(o *conn.Options) { o.Discover = d })
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
// an error naming --hub and the likely firewall cause, and sends nothing:
// no request reaches any hub and nothing is saved.
func TestEnrollWithoutAHubAndNoneFoundSendsNothing(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	nameHub(t, h, "hub.test")
	other := newCA(t)
	imp := startImpostor(t, serverCert(t, other, []string{"hub.test", "127.0.0.1"}, other.Cert.Raw))
	stray := service(t, h.addr, "hub.test")
	stray.Name = "printer._ipp._tcp.local."
	cases := []struct {
		name     string
		services []conn.Service
		err      error
		errKind  string
	}{
		{"nothing answers", nil, nil, "timeout"},
		{"only a stray service", []conn.Service{stray}, nil, "timeout"},
		{"only an impostor", []conn.Service{service(t, imp.addr, "hub.test")}, nil, "refused"},
		{"only a closed port", []conn.Service{service(t, closedAddr(t), "hub.test")}, nil, "refused"},
		{"no multicast", nil, errors.New("failed to bind to any multicast udp port"), "io"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDiscover{services: tc.services, err: tc.err}
			a := newAgent(t)
			_, err := a.enrollWith(t, "", h.token, func(o *conn.Options) { o.Discover = d })
			if !errors.Is(err, conn.ErrNoHubFound) || !strings.Contains(err.Error(), "--hub") ||
				!strings.Contains(err.Error(), "firewall") {
				t.Fatalf("enroll = %v, want ErrNoHubFound naming --hub and the firewall", err)
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

// A given --hub is used as it is, and an enrolled machine never browses.
func TestEnrollBrowsesOnlyWithoutAHubAndUnenrolled(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	d := &fakeDiscover{}
	a := newAgent(t)
	if _, err := a.enrollWith(t, h.addr, h.token, func(o *conn.Options) { o.Discover = d }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.enrollWith(t, "", h.token, func(o *conn.Options) { o.Discover = d }); !errors.Is(err, conn.ErrAlreadyEnrolled) {
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
