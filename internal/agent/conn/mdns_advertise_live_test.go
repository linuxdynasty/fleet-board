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
// with the SRV target, the port and the advertised address.
//
// "loopback" lists the loopback interface as the one to advertise on, with a
// documentation address, since the hub never advertises loopback itself.
// "chosen interface" is the real choice: this machine's interfaces and
// default route, browsed on the interface the hub picked; a host that does
// not let this process send multicast there fails the browse and the subtest
// skips with the library's error.
func TestLiveAdvertiserIsFoundByTheAgentBrowser(t *testing.T) {
	if os.Getenv("FLEET_MDNS_LIVE") == "" {
		t.Skip("set FLEET_MDNS_LIVE=1 to advertise and browse real multicast")
	}
	loName := loopbackName()
	loopback := discovery.Env{
		Ifaces: func() ([]discovery.Iface, error) {
			return []discovery.Iface{{Name: loName, Flags: net.FlagUp | net.FlagMulticast, Addrs: []netip.Addr{netip.MustParseAddr("192.0.2.10")}}}, nil
		},
		Route: func() discovery.Route { return discovery.Route{} },
	}
	for _, tc := range []struct {
		name, host string
		env        discovery.Env
		wantAddr   string // "" means whatever the hub chose
	}{{"loopback", "advlive-lo", loopback, "192.0.2.10"}, {"chosen interface", "advlive-def", discovery.Env{}, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			adv := discovery.New(discovery.Options{
				Enabled: true, Hostname: tc.host, Cert: servesAll{},
				Listener: &net.TCPAddr{IP: net.IPv6unspecified, Port: 7443}, Env: tc.env,
			})
			adv.Start()
			t.Cleanup(adv.Stop)
			rec, ok := adv.Advertised()
			if !ok {
				t.Skip("the hub chose not to advertise on this machine (no usable interface or multicast group)")
			}
			ifi, err := net.InterfaceByName(rec.Iface)
			if err != nil {
				t.Fatal(err)
			}
			d := conn.NewMDNSOn(ifi)
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
				t.Skipf("this host cannot browse multicast on %s (%v); not a result for the advertiser", rec.Iface, err)
			}
			if first < 0 {
				t.Fatalf("the browser did not find fleetd-%s on %s", tc.host, rec.Iface)
			}
			// The agent dials AddrV4 first, then AddrV6; the answer must hold
			// an address the hub advertised, in either family.
			dial := got.AddrV4
			if !dial.IsValid() {
				dial = got.AddrV6
			}
			advertised := false
			for _, ip := range rec.IPs {
				advertised = advertised || ip.String() == dial.String()
			}
			if got.Host != "fleetd-"+tc.host+".local." || got.Port != 7443 || !advertised || (tc.wantAddr != "" && dial.String() != tc.wantAddr) {
				t.Errorf("found %+v, want one of %v (%s)", got, rec.IPs, tc.wantAddr)
			}
			t.Logf("found %s on %s after %v, address %s", got.Name, rec.Iface, first, dial)

			adv.Stop()
			if again, _, err := find(); err == nil && again.Name != "" {
				t.Errorf("still found after Stop: %+v", again)
			}
		})
	}
}
