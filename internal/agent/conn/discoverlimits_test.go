package conn_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// mDNS answers that accept and never reply cannot keep the configured
// address waiting: each is cut at its own budget, and the mDNS phase as a
// whole is capped, after which the configured address is tried.
func TestSilentMDNSAnswersDoNotHoldUpTheConfiguredAddress(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	removeState(t, a, conn.HubAddressFile)
	var svcs []conn.Service
	for i := range 8 {
		svcs = append(svcs, service(t, blackhole(t), "hole"+strconv.Itoa(i)+".test"))
	}
	d := &fakeDiscover{services: svcs}
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		useMDNS(o, d)
		conn.SetMDNSLimits(o, 200*time.Millisecond, 700*time.Millisecond)
		o.Identity.HubAddresses = []string{h.addr}
	})
	h.connected(t, 1)
	r.connected(t)
	// No elapsed-time bound: eight silent answers at 200 ms each fit any bound
	// a loaded machine allows. The attempts below prove the phase cap cut them.
	got := attempts(t, a)
	mdns := slices.IndexFunc(got, func(at [2]string) bool { return at[0] != "mdns" })
	if mdns < 1 || mdns > 5 || got[len(got)-1] != [2]string{"configured", h.addr} {
		t.Errorf("attempts = %v, want a few silent mdns answers cut by the phase cap, then %s", got, h.addr)
	}
	if c := a.events(t, "connect.mdns_cut"); len(c) != 1 {
		t.Errorf("connect.mdns_cut = %v, want one", c)
	}
}

// fleet_agent_discovery_seconds counts an attempt that found nothing as
// result="none", and a stop during the search not at all.
func TestTheDiscoveryMetricCountsAMissAndNotAStop(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	writeState(t, a, conn.HubAddressFile, closedAddr(t)+"\n")
	r := startLoop(t, a, func(o *conn.LoopOptions) { useMDNS(o, &fakeDiscover{}) })
	waitFor(t, 10*time.Second, "two missed attempts", func() bool {
		n, _ := strconv.Atoi(metric(scrape(t, a), "fleet_agent_discovery_seconds_count", `result="none"`))
		return n >= 2
	})
	r.cancel()
	_ = r.result(t, 10*time.Second)
	m := scrape(t, a)
	none, _ := strconv.Atoi(metric(m, "fleet_agent_discovery_seconds_count", `result="none"`))
	tries := len(a.events(t, "connect.backoff"))
	if none != tries && none != tries+1 {
		t.Errorf("result=none counted %d for %d failed attempts", none, tries)
	}
	if v := metric(m, "fleet_agent_discovery_seconds_count", `result="found"`); v != "" {
		t.Errorf("result=found = %s, want none", v)
	}

	// A stop while the browse is still running is not a miss.
	b := enrolled(t, h)
	writeState(t, b, conn.HubAddressFile, closedAddr(t)+"\n")
	held := &fakeDiscover{hold: true}
	r2 := startLoop(t, b, func(o *conn.LoopOptions) { useMDNS(o, held); o.BrowseTimeout = time.Minute })
	waitFor(t, 10*time.Second, "the browse", func() bool { return held.count() == 1 })
	r2.cancel()
	_ = r2.result(t, 10*time.Second)
	if v := metric(scrape(t, b), "fleet_agent_discovery_seconds_count"); v != "" {
		t.Errorf("a stop during the search counted %s", v)
	}
}
