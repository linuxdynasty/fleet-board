package discovery_test

import (
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"

	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

func TestStartTwiceAdvertisesOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.start(t)
	a.Start()
	if len(r.resp.served) != 1 || len(r.resp.checked) != 2 {
		t.Errorf("served %d times with %d bind checks, want 1 and 2", len(r.resp.served), len(r.resp.checked))
	}
	if v := metric(r.scrape(t), "fleet_hub_discovery_advertising"); v != 1 {
		t.Errorf("fleet_hub_discovery_advertising = %v, want 1", v)
	}
	if rec, ok := a.Advertised(); !ok || rec.Iface != "en0" {
		t.Errorf("Advertised() = %+v, %v", rec, ok)
	}
	a.Stop()
	if _, ok := a.Advertised(); ok {
		t.Error("Advertised() is true after Stop")
	}
}

func TestANilCertificateRefusesInsteadOfPanicking(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Cert = nil
	r.start(t)
	ev := r.logs.events(t, "discovery.advertise_refused")
	if len(ev) != 1 || ev[0]["reason"] != "cert_missing_target" || len(r.resp.served) != 0 {
		t.Errorf("discovery.advertise_refused = %v, served %d", ev, len(r.resp.served))
	}
}

// Of several interfaces only the chosen one is joined and advertised.
func TestTheChosenInterfaceIsTheOneJoined(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.Env.Route = func() discovery.Route { return discovery.Route{V4: netip.MustParseAddr("10.1.1.5")} }
	r.opts.Env.Ifaces = func() ([]discovery.Iface, error) {
		return []discovery.Iface{nic("en0", up, "192.168.1.5"), nic("en1", up, "10.1.1.5"), nic("bridge100", up, "192.168.64.1")}, nil
	}
	r.start(t)
	if len(r.resp.served) != 1 || r.resp.served[0].Iface != "en1" || !reflect.DeepEqual(ips(r.resp.served[0]), []string{"10.1.1.5"}) {
		t.Errorf("served %+v", r.resp.served)
	}
	if !reflect.DeepEqual(r.resp.checked, []string{"en1/udp4", "en1/udp6"}) {
		t.Errorf("bind checks %q, want the chosen interface only", r.resp.checked)
	}
}

// The real readers touch no multicast: they list interfaces and ask the
// kernel for a route. Whatever the machine has, the loopback interface is
// listed as loopback, and a route source is never loopback or unspecified.
func TestTheMachineReadersAreSane(t *testing.T) {
	t.Parallel()
	ifaces, err := discovery.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	loopbacks := 0
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 {
			loopbacks++
		}
	}
	if loopbacks == 0 {
		t.Error("no loopback interface among the machine's interfaces")
	}
	route := discovery.RouteSource()
	for _, a := range []netip.Addr{route.V4, route.V6} {
		if a.IsValid() && (a.IsLoopback() || a.IsUnspecified() || a.Zone() != "") {
			t.Errorf("route source %v", a)
		}
	}
}

// failingMeter cannot register an instrument.
type failingMeter struct{ noop.Meter }

func (failingMeter) Int64Counter(string, ...otelmetric.Int64CounterOption) (otelmetric.Int64Counter, error) {
	return nil, errors.New("instrument refused")
}

// If the signals cannot be registered the hub still advertises, and says once
// that it counts nothing.
func TestAMeterThatCannotRegisterCostsTheCountsNotTheAdvertisement(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts = discovery.WithMeter(r.opts, failingMeter{})
	a := r.start(t)
	ev := r.logs.events(t, "discovery.advertise_failed")
	if len(ev) != 1 || ev[0]["step"] != "telemetry" {
		t.Errorf("discovery.advertise_failed = %v, want one with step telemetry", ev)
	}
	if len(r.resp.served) != 1 {
		t.Error("did not advertise without its counters")
	}
	if v := metric(r.scrape(t), "fleet_hub_discovery_advertise_total", `result="started"`); v != -1 {
		t.Errorf("advertise_total = %v, want no series", v)
	}
	a.Stop()
}
