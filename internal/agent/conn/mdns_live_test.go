package conn_test

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/mdns"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// These tests use real multicast on this machine's network, so they run
// only when asked. Neither touches tmux or sleep.

// FLEET_MDNS_LIVE=1: a fake fleetd advertised on this machine with the same
// library is found by the agent's own browser, with its name, port and
// address.
func TestLiveMDNSFindsALocalAdvertiser(t *testing.T) {
	if os.Getenv("FLEET_MDNS_LIVE") == "" {
		t.Skip("set FLEET_MDNS_LIVE=1 to browse real multicast")
	}
	svc, err := mdns.NewMDNSService("fleetd-livetest", "_fleetd._tcp", "", "fleetd-livetest.local.", 7443,
		[]net.IP{net.IPv4(127, 0, 0, 1)}, []string{"livetest=1"})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mdns.NewServer(&mdns.Config{Zone: svc, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	var got []conn.Service
	if err := conn.NewMDNS().Browse(context.Background(), conn.DefaultBrowseTimeout, func(s conn.Service) {
		got = append(got, s)
	}); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(got, func(s conn.Service) bool { return strings.HasPrefix(s.Name, "fleetd-livetest.") })
	if i < 0 {
		t.Fatalf("the browse found %d services, not fleetd-livetest: %+v", len(got), got)
	}
	if s := got[i]; s.Host != "fleetd-livetest.local." || s.Port != 7443 || s.AddrV4.String() != "127.0.0.1" {
		t.Errorf("found %+v", s)
	}
}

// FLEET_MDNS_WIFI_HUB=<instance>: plan row 4's Wi-Fi measurement, run on a
// worker against a real hub on Wi-Fi advertising that instance name. Fifty
// browses of 2 s, 3 s apart; it reports found and the first answer's p50,
// p99 and worst, and fails on any miss or a first answer over 1 s (ADR 0028
// open question 2: either goes back for a ruling before row 4 merges).
func TestWiFiHubBrowseMeasurement(t *testing.T) {
	instance := os.Getenv("FLEET_MDNS_WIFI_HUB")
	if instance == "" {
		t.Skip("set FLEET_MDNS_WIFI_HUB=<hub instance name> to measure a Wi-Fi hub")
	}
	const runs = 50
	var firsts []time.Duration
	for n := range runs {
		begin := time.Now()
		first := time.Duration(-1)
		err := conn.NewMDNS().Browse(context.Background(), conn.DefaultBrowseTimeout, func(s conn.Service) {
			if first < 0 && strings.HasPrefix(strings.ReplaceAll(s.Name, `\ `, " "), instance+".") {
				first = time.Since(begin)
			}
		})
		if err != nil {
			t.Fatalf("browse %d: %v", n+1, err)
		}
		if first >= 0 {
			firsts = append(firsts, first)
		}
		time.Sleep(time.Until(begin.Add(3 * time.Second)))
	}
	slices.Sort(firsts)
	pick := func(q float64) time.Duration {
		if len(firsts) == 0 {
			return -1
		}
		return firsts[min(len(firsts)-1, int(q*float64(len(firsts))))]
	}
	t.Logf("found %d of %d; first answer p50 %v, p99 %v, worst %v", len(firsts), runs, pick(0.5), pick(0.99), pick(1))
	if len(firsts) != runs || pick(1) > time.Second {
		t.Errorf("a miss or a first answer over 1 s: this goes back for a ruling before row 4 merges (ADR 0028)")
	}
}
