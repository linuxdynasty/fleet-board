package daemon_test

import (
	"net/netip"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

// fakeResponder stands in for the multicast sockets, so a daemon test sends
// nothing and joins no group.
type fakeResponder struct {
	mu      sync.Mutex
	served  []discovery.Records
	stopped int
}

func (f *fakeResponder) CheckBind(string) error { return nil }

func (f *fakeResponder) Serve(r discovery.Records) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served = append(f.served, r)
	return func() { f.mu.Lock(); f.stopped++; f.mu.Unlock() }, nil
}

func (f *fakeResponder) state() (served []discovery.Records, stopped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]discovery.Records(nil), f.served...), f.stopped
}

// The owner's own names never cost the hub its mDNS name: the SRV target is
// in the certificate init issues, so the first run does not re-issue it.
func TestInitKeepsTheServiceTargetWhenAgentNamesAreSet(t *testing.T) {
	t.Parallel()
	h := newHub(t, "tls: {agent_names: [\"hub.example\", \"127.0.0.1\"]}\n")
	h.init(t)
	id := h.storedIdentity(t)
	want := []string{"hub.example", "127.0.0.1", serviceTarget}
	if !reflect.DeepEqual(id.ServerNames, want) {
		t.Errorf("stored SAN list %q, want %q", id.ServerNames, want)
	}
	pool := caPool(t, id)
	r := h.start(t, h.options())
	agent := "127.0.0.1:" + port(r.addrs.Agent)
	for _, name := range []string{serviceTarget, "hub.example"} {
		if _, err := dialAgent(t, pool, agent, name, 0); err != nil {
			t.Errorf("TLS to %s: %v", name, err)
		}
	}
	if n := len(h.logs.events(t, "tls.server_cert_issued")); n != 0 {
		t.Errorf("the first run re-issued the certificate %d times", n)
	}
}

// A hub whose certificate has only the old names gets the target on the next
// start, in one re-issue logged as names_changed.
func TestAnOlderCertificateGainsTheServiceTargetInOneReissue(t *testing.T) {
	t.Parallel()
	h := newHub(t, "tls: {agent_names: [\"hub.example\"]}\n")
	h.init(t)
	before := h.storedIdentity(t)
	h.writeConfig(t, "tls: {agent_names: [\"hub.example\", \"10.0.0.7\"]}\n")
	r := h.start(t, h.options())
	agent := "127.0.0.1:" + port(r.addrs.Agent)
	if _, err := dialAgent(t, caPool(t, before), agent, serviceTarget, 0); err != nil {
		t.Errorf("TLS to the SRV target after the names changed: %v", err)
	}
	ev := h.logs.events(t, "tls.server_cert_issued")
	if len(ev) != 1 || ev[0]["reason"] != "names_changed" {
		t.Errorf("tls.server_cert_issued = %v, want one names_changed", ev)
	}
}

func TestRunAdvertisesTheAgentPortAndStopsAtShutdown(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	r := h.start(t, h.options())

	served, stopped := h.resp.state()
	if len(served) != 1 || stopped != 0 {
		t.Fatalf("after start: served %d, stopped %d; want 1, 0", len(served), stopped)
	}
	got := served[0]
	if got.Target != serviceTarget+"." || got.Instance != "fleetd-fleet-test-host" {
		t.Errorf("target %q, instance %q", got.Target, got.Instance)
	}
	if want, _ := strconv.Atoi(port(r.addrs.Agent)); got.Port != want {
		t.Errorf("port %d, want the agent listener's %d", got.Port, want)
	}
	if len(got.IPs) != 1 || got.IPs[0].String() != "192.168.1.5" || !reflect.DeepEqual(got.TXT, []string{"proto=1-1"}) {
		t.Errorf("addresses %v, TXT %q", got.IPs, got.TXT)
	}
	if n := len(h.logs.events(t, "discovery.advertise_started")); n != 1 {
		t.Errorf("discovery.advertise_started logged %d times, want 1", n)
	}

	r.stop(t)
	if _, stopped := h.resp.state(); stopped != 1 {
		t.Errorf("responder stopped %d times at shutdown, want 1", stopped)
	}
	if n := len(h.logs.events(t, "discovery.advertise_stopped")); n != 1 {
		t.Errorf("discovery.advertise_stopped logged %d times, want 1", n)
	}
}

func TestDiscoveryMDNSFalseAdvertisesNothing(t *testing.T) {
	t.Parallel()
	h := newHub(t, "discovery: {mdns: false}\n")
	h.init(t)
	h.start(t, h.options()).stop(t)
	if served, _ := h.resp.state(); len(served) != 0 {
		t.Error("advertised with discovery.mdns false")
	}
	if n := len(h.logs.events(t, "discovery.advertise_disabled")); n != 1 {
		t.Errorf("discovery.advertise_disabled logged %d times, want 1", n)
	}
}

// A hub with no unicast non-loopback address still serves; it says why it
// is not advertising.
func TestALoopbackOnlyHostServesAndAdvertisesNothing(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	opts.Discovery.Addrs = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil }
	h.start(t, opts).stop(t)
	if served, _ := h.resp.state(); len(served) != 0 {
		t.Error("advertised a loopback-only host")
	}
	ev := h.logs.events(t, "discovery.advertise_refused")
	if len(ev) != 1 || ev[0]["reason"] != "no_addresses" {
		t.Errorf("discovery.advertise_refused = %v", ev)
	}
}
