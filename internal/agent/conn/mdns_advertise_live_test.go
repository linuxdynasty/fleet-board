package conn_test

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

// servesAll is a certificate that names everything the advertiser asks for.
type servesAll struct{}

func (servesAll) Serves(string) bool { return true }

// FLEET_MDNS_LIVE=1: the hub's real advertiser (internal/hub/discovery, the
// same library and records fleetd uses) is found by the agent's own browser,
// with the SRV target, the port and the advertised address. The advertised
// address is a documentation address, since the hub never advertises loopback.
// It runs on loopback, and on the default multicast interface, where a host
// that does not let this process send multicast fails the browse and the
// subtest skips with the library's error.
func TestLiveAdvertiserIsFoundByTheAgentBrowser(t *testing.T) {
	if os.Getenv("FLEET_MDNS_LIVE") == "" {
		t.Skip("set FLEET_MDNS_LIVE=1 to advertise and browse real multicast")
	}
	lo, err := net.InterfaceByName(loopbackName())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, host string
		iface      *net.Interface
	}{{"loopback", "advlive-lo", lo}, {"default interface", "advlive-def", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			adv, err := discovery.New(discovery.Options{
				Enabled: true, Hostname: tc.host, Cert: servesAll{},
				Listener: &net.TCPAddr{IP: net.IPv6unspecified, Port: 7443},
				Env: discovery.Env{
					Responder: discovery.NewResponder(tc.iface),
					Addrs:     func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.0.2.10")}, nil },
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			adv.Start()
			t.Cleanup(adv.Stop)
			d := conn.NewMDNS()
			if tc.iface != nil {
				d = conn.NewMDNSOn(tc.iface)
			}
			find := func() (conn.Service, time.Duration, error) {
				var got conn.Service
				first := time.Duration(-1)
				start := time.Now()
				err := d.Browse(context.Background(), conn.DefaultBrowseTimeout, func(s conn.Service) {
					if strings.HasPrefix(s.Name, "fleetd-"+tc.host+".") {
						got, first = s, time.Since(start)
					}
				})
				return got, first, err
			}
			got, first, err := find()
			if err != nil {
				t.Skipf("this host cannot browse multicast here (%v); not a result for the advertiser", err)
			}
			if first < 0 {
				t.Fatalf("the browser did not find fleetd-%s", tc.host)
			}
			if got.Host != "fleetd-"+tc.host+".local." || got.Port != 7443 || got.AddrV4.String() != "192.0.2.10" {
				t.Errorf("found %+v", got)
			}
			t.Logf("found %s after %v", got.Name, first)

			adv.Stop()
			if again, _, err := find(); err == nil && again.Name != "" {
				t.Errorf("still found after Stop: %+v", again)
			}
		})
	}
}
