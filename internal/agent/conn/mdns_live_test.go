package conn_test

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"runtime"
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
// address: on loopback, and on the default multicast interface, where a
// host that does not let this process send multicast fails the query.
func TestLiveMDNSFindsALocalAdvertiser(t *testing.T) {
	if os.Getenv("FLEET_MDNS_LIVE") == "" {
		t.Skip("set FLEET_MDNS_LIVE=1 to browse real multicast")
	}
	lo, err := net.InterfaceByName(loopbackName())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		iface *net.Interface
	}{{"loopback", lo}, {"default interface", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			instance := "fleetd-livetest-" + strings.ReplaceAll(tc.name, " ", "-")
			svc, err := mdns.NewMDNSService(instance, "_fleetd._tcp", "", instance+".local.", 7443,
				[]net.IP{net.IPv4(127, 0, 0, 1)}, []string{"livetest=1"})
			if err != nil {
				t.Fatal(err)
			}
			srv, err := mdns.NewServer(&mdns.Config{Zone: svc, Iface: tc.iface, Logger: log.New(io.Discard, "", 0)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Shutdown() })
			d := conn.NewMDNS()
			if tc.iface != nil {
				d = conn.NewMDNSOn(tc.iface)
			}
			var got []conn.Service
			start := time.Now()
			first := time.Duration(-1)
			err = d.Browse(context.Background(), conn.DefaultBrowseTimeout, func(s conn.Service) {
				got = append(got, s)
				if first < 0 && strings.HasPrefix(s.Name, instance+".") {
					first = time.Since(start)
				}
			})
			if err != nil {
				t.Fatalf("browse: %v", err)
			}
			i := slices.IndexFunc(got, func(s conn.Service) bool { return strings.HasPrefix(s.Name, instance+".") })
			if i < 0 {
				t.Fatalf("the browse found %d services, not %s: %+v", len(got), instance, got)
			}
			if s := got[i]; s.Host != instance+".local." || s.Port != 7443 || s.AddrV4.String() != "127.0.0.1" {
				t.Errorf("found %+v", s)
			}
			t.Logf("found %s after %v", instance, first)
		})
	}
}

func loopbackName() string {
	if runtime.GOOS == "darwin" {
		return "lo0"
	}
	return "lo"
}

// FLEET_MDNS_WIFI_HUB=<instance>: the Wi-Fi measurement of ADR 0028 open
// question 2, run on a worker against a real hub on Wi-Fi advertising that
// instance name; tracked in #154, row 5. Fifty browses of 2 s, 3 s apart; it
// reports found and the first answer's p50, p99 and worst, and fails on any
// miss or a first answer over 1 s. The time is when the agent's browser
// hands the answer over, which is when the query that brought it ends
// (ADR 0030): up to one query after the reply arrived. A time near 0.5 s
// means the reply came in the first query (under 0.5 s); one near 2 s means
// it came in the second, between 0.5 and 2 s, so over 1 s cannot be ruled
// out and counts as over.
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
		t.Errorf("a miss or a first answer over 1 s (as handed over): tracked in #154, row 5")
	}
}
