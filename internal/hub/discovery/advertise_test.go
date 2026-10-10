package discovery_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// events are the log lines whose msg is name.
func (b *safeBuffer) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", sc.Text(), err)
		}
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

// fakeResponder stands in for the multicast sockets: default tests send
// nothing.
type fakeResponder struct {
	mu        sync.Mutex
	bindErr   map[string]error // by family
	serveErr  error
	checked   []string
	served    []discovery.Records
	stopCalls int
}

func (f *fakeResponder) CheckBind(family string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, family)
	return f.bindErr[family]
}

func (f *fakeResponder) Serve(r discovery.Records) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.serveErr != nil {
		return nil, f.serveErr
	}
	f.served = append(f.served, r)
	return func() { f.mu.Lock(); f.stopCalls++; f.mu.Unlock() }, nil
}

func (f *fakeResponder) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalls
}

// certNames is a certificate that serves exactly the names it lists.
type certNames []string

func (c certNames) Serves(name string) bool {
	for _, n := range c {
		if n == name {
			return true
		}
	}
	return false
}

type rig struct {
	resp *fakeResponder
	logs *safeBuffer
	tel  *telemetry.Telemetry
	opts discovery.Options
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{resp: &fakeResponder{}, logs: &safeBuffer{}}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: r.logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	r.tel = tel
	r.opts = discovery.Options{
		Enabled:  true,
		Hostname: "Fleet-Test-Host.local",
		Listener: &net.TCPAddr{IP: net.IPv6unspecified, Port: 7443},
		Cert:     certNames{"fleet-test-host", "fleetd-fleet-test-host.local"},
		Env: discovery.Env{
			Responder: r.resp,
			Addrs:     func() ([]netip.Addr, error) { return addrs("192.168.1.5", "127.0.0.1", "fe80::1"), nil },
		},
		Telemetry: tel,
	}
	return r
}

func (r *rig) start(t *testing.T) *discovery.Advertiser {
	t.Helper()
	a, err := discovery.New(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	t.Cleanup(a.Stop)
	return a
}

func (r *rig) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	r.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

// metric is the value of the series whose line starts with name and holds
// every fragment in want; -1 when there is none.
func metric(body, name string, want ...string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if ok {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}

func TestStartAdvertisesTheRecordsAndLogsOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start(t)

	if len(r.resp.served) != 1 {
		t.Fatalf("served %d times, want 1", len(r.resp.served))
	}
	got := r.resp.served[0]
	if got.Target != "fleetd-fleet-test-host.local." || got.Port != 7443 || !reflect.DeepEqual(got.TXT, []string{"proto=1-1"}) {
		t.Errorf("records %+v", got)
	}
	if ip := ips(t, got); !reflect.DeepEqual(ip, []string{"192.168.1.5"}) {
		t.Errorf("addresses %q, want only 192.168.1.5", ip)
	}
	if !reflect.DeepEqual(r.resp.checked, []string{"udp4", "udp6"}) {
		t.Errorf("bind checks %q, want one per family", r.resp.checked)
	}
	ev := r.logs.events(t, "discovery.advertise_started")
	if len(ev) != 1 || ev[0]["port"] != float64(7443) || ev[0]["addresses"] != float64(1) {
		t.Errorf("discovery.advertise_started = %v", ev)
	}
	body := r.scrape(t)
	if v := metric(body, "fleet_hub_discovery_advertising"); v != 1 {
		t.Errorf("fleet_hub_discovery_advertising = %v, want 1\n%s", v, body)
	}
	if v := metric(body, "fleet_hub_discovery_advertise_total", `result="started"`); v != 1 {
		t.Errorf("advertise_total{started} = %v, want 1", v)
	}
	// The log carries counts, never the name list or an address.
	for _, leak := range []string{"fleet-test-host", "192.168.1.5"} {
		if strings.Contains(r.logs.String(), leak) {
			t.Errorf("the log holds %q", leak)
		}
	}
}

func TestStopEndsTheAdvertisingOnceAndLogsIt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.start(t)
	a.Stop()
	a.Stop()
	if r.resp.stops() != 1 {
		t.Errorf("responder stopped %d times, want 1", r.resp.stops())
	}
	if n := len(r.logs.events(t, "discovery.advertise_stopped")); n != 1 {
		t.Errorf("discovery.advertise_stopped logged %d times, want 1", n)
	}
	if v := metric(r.scrape(t), "fleet_hub_discovery_advertising"); v != 0 {
		t.Errorf("fleet_hub_discovery_advertising = %v after Stop, want 0", v)
	}
}

func TestStopWithoutAStartedAdvertisementSaysNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Cert = certNames{}
	a := r.start(t)
	a.Stop()
	if n := len(r.logs.events(t, "discovery.advertise_stopped")); n != 0 {
		t.Errorf("discovery.advertise_stopped logged %d times with nothing advertised", n)
	}
}

// A certificate that cannot name the SRV target (a Certs that could not
// re-issue) means an agent that follows the answer fails TLS: the hub does
// not advertise it.
func TestRefusesWhenTheCertificateLacksTheTarget(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Cert = certNames{"fleet-test-host", "fleet-test-host.local"}
	r.start(t)
	if len(r.resp.served) != 0 || len(r.resp.checked) != 0 {
		t.Errorf("the responder was used: served %d, bind checks %q", len(r.resp.served), r.resp.checked)
	}
	ev := r.logs.events(t, "discovery.advertise_refused")
	if len(ev) != 1 || ev[0]["reason"] != "cert_missing_target" {
		t.Errorf("discovery.advertise_refused = %v", ev)
	}
	body := r.scrape(t)
	if v := metric(body, "fleet_hub_discovery_advertise_total", `result="refused"`, `reason="cert_missing_target"`); v != 1 {
		t.Errorf("advertise_total{refused, cert_missing_target} = %v, want 1\n%s", v, body)
	}
	if v := metric(body, "fleet_hub_discovery_advertising"); v != 0 {
		t.Errorf("fleet_hub_discovery_advertising = %v, want 0", v)
	}
}

func TestRefusalsAreLoggedOnceWithTheirReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*rig)
		reason string
	}{
		{"loopback-only host", func(r *rig) {
			r.opts.Env.Addrs = func() ([]netip.Addr, error) { return addrs("127.0.0.1", "::1"), nil }
		}, "no_addresses"},
		{"loopback listener", func(r *rig) { r.opts.Listener = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7443} }, "no_addresses"},
		{"no host name", func(r *rig) { r.opts.Hostname = "" }, "no_hostname"},
		{"no listener", func(r *rig) { r.opts.Listener = nil }, "bad_listener"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			tc.mutate(r)
			r.start(t)
			if len(r.resp.served) != 0 {
				t.Error("advertised anyway")
			}
			ev := r.logs.events(t, "discovery.advertise_refused")
			if len(ev) != 1 || ev[0]["reason"] != tc.reason {
				t.Errorf("discovery.advertise_refused = %v, want one with reason %s", ev, tc.reason)
			}
		})
	}
}

func TestDisabledAdvertisesNothingAndTouchesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Enabled = false
	r.opts.Env.Addrs = func() ([]netip.Addr, error) { t.Error("listed the interfaces while disabled"); return nil, nil }
	r.start(t)
	if len(r.resp.served) != 0 || len(r.resp.checked) != 0 {
		t.Error("the responder was used while disabled")
	}
	if n := len(r.logs.events(t, "discovery.advertise_disabled")); n != 1 {
		t.Errorf("discovery.advertise_disabled logged %d times, want 1", n)
	}
	if v := metric(r.scrape(t), "fleet_hub_discovery_advertise_total", `result="disabled"`); v != 1 {
		t.Errorf("advertise_total{disabled} = %v, want 1", v)
	}
}

// One family that cannot bind is logged once and the hub advertises on the
// other; the library alone would drop it without a word (ADR 0028).
func TestOneFamilyThatCannotBindIsLoggedOnceAndTheOtherServes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.resp.bindErr = map[string]error{"udp6": errors.New("listen udp6: address in use")}
	r.start(t)
	if len(r.resp.served) != 1 {
		t.Fatal("did not advertise with one family bound")
	}
	ev := r.logs.events(t, "discovery.family_unavailable")
	if len(ev) != 1 || ev[0]["family"] != "udp6" || ev[0]["err_kind"] == nil {
		t.Errorf("discovery.family_unavailable = %v", ev)
	}
	if strings.Contains(r.logs.String(), "address in use") {
		t.Error("the log holds the error text")
	}
	if n := len(r.logs.events(t, "discovery.advertise_started")); n != 1 {
		t.Errorf("discovery.advertise_started logged %d times, want 1", n)
	}
}

func TestNoFamilyThatCanBindFailsTheAdvertisementNotTheHub(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	boom := errors.New("no multicast")
	r.resp.bindErr = map[string]error{"udp4": boom, "udp6": boom}
	a := r.start(t)
	if len(r.resp.served) != 0 {
		t.Error("served with no family bound")
	}
	ev := r.logs.events(t, "discovery.advertise_failed")
	if len(ev) != 1 || ev[0]["step"] != "bind" {
		t.Errorf("discovery.advertise_failed = %v", ev)
	}
	if n := len(r.logs.events(t, "discovery.family_unavailable")); n != 2 {
		t.Errorf("discovery.family_unavailable logged %d times, want one per family", n)
	}
	if v := metric(r.scrape(t), "fleet_hub_discovery_advertise_total", `result="failed"`); v != 1 {
		t.Errorf("advertise_total{failed} = %v, want 1", v)
	}
	a.Stop() // nothing to stop, and no panic
}

func TestAResponderThatCannotStartIsLoggedOnceAndTheHubKeepsServing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.resp.serveErr = errors.New("no multicast listeners could be started")
	a := r.start(t)
	ev := r.logs.events(t, "discovery.advertise_failed")
	if len(ev) != 1 || ev[0]["step"] != "serve" {
		t.Errorf("discovery.advertise_failed = %v", ev)
	}
	if v := metric(r.scrape(t), "fleet_hub_discovery_advertising"); v != 0 {
		t.Errorf("fleet_hub_discovery_advertising = %v, want 0", v)
	}
	a.Stop()
	if n := len(r.logs.events(t, "discovery.advertise_stopped")); n != 0 {
		t.Errorf("discovery.advertise_stopped logged %d times", n)
	}
}

func TestAnInterfaceListThatFailsIsLoggedAsAFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Env.Addrs = func() ([]netip.Addr, error) { return nil, errors.New("route: operation not permitted") }
	r.start(t)
	ev := r.logs.events(t, "discovery.advertise_failed")
	if len(ev) != 1 || ev[0]["step"] != "addresses" {
		t.Errorf("discovery.advertise_failed = %v", ev)
	}
	if len(r.resp.served) != 0 {
		t.Error("advertised without addresses")
	}
}
