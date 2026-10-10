package gateway_test

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
)

// countingResponder fails the test if the advertiser touches the network.
type countingResponder struct{ t *testing.T }

func (r countingResponder) CheckBind(string, string) error { r.t.Error("a bind check ran"); return nil }

func (r countingResponder) Serve(discovery.Records) (func(), error) {
	r.t.Error("Serve ran")
	return func() {}, nil
}

const serviceTarget = "fleetd-fleet-test-host.local"

// Serves is what the mDNS advertiser asks before it points agents at the SRV
// target: it reads the certificate served now, not the wanted names.
func TestServesReadsTheCertificateServedNow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	names := append([]string{serviceTarget}, defaultNames...)
	c := f.certs(t, names, wallNow)
	// Before Ensure the stored certificate (issued without the target) serves.
	for name, want := range map[string]bool{"fleet-test-host": true, "FLEET-TEST-HOST.local": true, serviceTarget: false, "127.0.0.1": true, "other.local": false} {
		if got := c.Serves(name); got != want {
			t.Errorf("before the re-issue Serves(%q) = %v, want %v", name, got, want)
		}
	}
	if err := c.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !c.Serves(serviceTarget) {
		t.Error("Serves(target) is false after the re-issue added it")
	}
}

// A Certs that cannot re-issue keeps serving the old certificate, and Serves
// says so: the hub then refuses to advertise a name agents cannot verify.
func TestServesIsFalseWhenTheReissueFailed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := f.certs(t, append([]string{serviceTarget}, defaultNames...), wallNow)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure succeeded with the store closed")
	}
	if c.Serves(serviceTarget) {
		t.Error("Serves(target) is true though the re-issue failed")
	}
}

// The acceptance case for the refusal: a real Certs whose store cannot take
// the re-issue still serves the old names, and the advertiser built on it
// refuses, logs once, and sends nothing.
func TestTheAdvertiserRefusesOnACertsThatCannotReissue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := f.certs(t, append([]string{serviceTarget}, defaultNames...), wallNow)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.Ensure(context.Background()) // fails; the old certificate stays
	adv := discovery.New(discovery.Options{
		Enabled: true, Hostname: "fleet-test-host.local", Cert: c, Telemetry: f.tel,
		Listener: &net.TCPAddr{IP: net.IPv6unspecified, Port: 7443},
		Env: discovery.Env{
			Responder: countingResponder{t},
			Ifaces: func() ([]discovery.Iface, error) {
				return []discovery.Iface{{Name: "en0", Flags: net.FlagUp | net.FlagMulticast, Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.5")}}}, nil
			},
			Route: func() discovery.Route { return discovery.Route{} },
		},
	})
	adv.Start()
	adv.Stop()
	logs := f.logs.String()
	if n := strings.Count(logs, `"msg":"discovery.advertise_refused"`); n != 1 || !strings.Contains(logs, `"reason":"cert_missing_target"`) {
		t.Errorf("want one discovery.advertise_refused with cert_missing_target; logs:\n%s", logs)
	}
}
