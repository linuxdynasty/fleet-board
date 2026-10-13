package discovery_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

// A network change (new addresses) re-advertises: the old serve stops and
// the new records are served, counted and logged once (02j "Discovery").
func TestRecheckReadvertisesOnANetworkChange(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ifaces := lanIfaces
	r.opts.Env.Ifaces = func() ([]discovery.Iface, error) { return ifaces, nil }
	a := discovery.New(r.opts)
	a.Start()
	if len(r.resp.served) != 1 {
		t.Fatalf("served %d times, want 1", len(r.resp.served))
	}
	ifaces = []discovery.Iface{nic("en1", up, "10.0.0.7")}
	a.Recheck()
	if got := r.resp.stops(); got != 1 {
		t.Errorf("old serve stopped %d times, want 1", got)
	}
	if len(r.resp.served) != 2 {
		t.Fatalf("served %d times, want 2", len(r.resp.served))
	}
	if ip := ips(r.resp.served[1]); len(ip) != 1 || ip[0] != "10.0.0.7" || r.resp.served[1].Iface != "en1" {
		t.Errorf("re-advertised %+v, want only 10.0.0.7 on en1", r.resp.served[1])
	}
	ev := r.logs.events(t, "discovery.advertise_refreshed")
	if len(ev) != 1 || ev[0]["port"] != float64(7443) || ev[0]["addresses"] != float64(1) {
		t.Errorf("discovery.advertise_refreshed = %v, want one line with port 7443 and 1 address", ev)
	}
	body := r.scrape(t)
	if v := metric(body, "fleet_hub_discovery_advertise_total", `result="refreshed"`); v != 1 {
		t.Errorf("advertise_total{refreshed} = %v, want 1\n%s", v, body)
	}
	if v := metric(body, "fleet_hub_discovery_advertising"); v != 1 {
		t.Errorf("fleet_hub_discovery_advertising = %v, want 1\n%s", v, body)
	}
	a.Stop()
}

// Nothing changed: no re-serve, no new line, no new count.
func TestRecheckKeepsServingWhenNothingChanged(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.start(t)
	a.Recheck()
	if len(r.resp.served) != 1 || r.resp.stops() != 0 {
		t.Errorf("served %d times with %d stops, want 1 and 0", len(r.resp.served), r.resp.stops())
	}
	if ev := r.logs.events(t, "discovery.advertise_refreshed"); len(ev) != 0 {
		t.Errorf("discovery.advertise_refreshed = %v, want none", ev)
	}
	body := r.scrape(t)
	if v := metric(body, "fleet_hub_discovery_advertise_total", `result="started"`); v != 1 {
		t.Errorf("advertise_total{started} = %v, want 1\n%s", v, body)
	}
	if v := metric(body, "fleet_hub_discovery_advertise_total", `result="refreshed"`); v != -1 {
		t.Errorf("advertise_total{refreshed} = %v, want none\n%s", v, body)
	}
}

// A recheck after Stop serves nothing: shutdown stays shut.
func TestRecheckAfterStopServesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.start(t)
	a.Stop()
	a.Recheck()
	if len(r.resp.served) != 1 || r.resp.stops() != 1 {
		t.Errorf("served %d times with %d stops, want 1 and 1", len(r.resp.served), r.resp.stops())
	}
}

// A hub that found no network at start advertises once one appears.
func TestRecheckStartsLateWhenTheNetworkAppears(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ifaces := []discovery.Iface{nic("lo0", up, "127.0.0.1")}
	r.opts.Env.Ifaces = func() ([]discovery.Iface, error) { return ifaces, nil }
	a := discovery.New(r.opts)
	a.Start()
	t.Cleanup(a.Stop)
	t.Cleanup(func() { r.checkNoErrorText(t) })
	if len(r.resp.served) != 0 {
		t.Fatalf("served %d times with loopback only, want 0", len(r.resp.served))
	}
	ifaces = lanIfaces
	a.Recheck()
	if len(r.resp.served) != 1 {
		t.Fatalf("served %d times after the network appeared, want 1", len(r.resp.served))
	}
	body := r.scrape(t)
	if v := metric(body, "fleet_hub_discovery_advertise_total", `result="started"`); v != 1 {
		t.Errorf("advertise_total{started} = %v, want 1\n%s", v, body)
	}
	if v := metric(body, "fleet_hub_discovery_advertising"); v != 1 {
		t.Errorf("fleet_hub_discovery_advertising = %v, want 1\n%s", v, body)
	}
}
