package conn_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// 02g A1 step 2: a token that names another CA is refused at the TLS
// handshake, before the token or CSR is sent.
func TestAWrongCAHashIsRefusedBeforeTheTokenIsSent(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	tok := h.TokenForCA(t, newCA(t).Cert.Raw, time.Now().Add(time.Hour))

	_, err := a.enroll(t, h.Addr, tok)
	if !errors.Is(err, conn.ErrHubUntrusted) {
		t.Fatalf("Enroll = %v, want ErrHubUntrusted", err)
	}
	if n := h.Requests(); n != 0 {
		t.Errorf("the hub served %d requests; the token reached it", n)
	}
	if h.TokenUsed(t, tok) || len(h.Events(t, "enroll.")) != 0 {
		t.Error("the hub saw an Enroll")
	}
	if lines := a.events(t, "enroll.hub_untrusted"); len(lines) != 1 || lines[0]["peer"] != h.Addr {
		t.Errorf("enroll.hub_untrusted lines %v, want one naming the peer", lines)
	}
	if _, err := conn.LoadIdentity(a.dir); !errors.Is(err, conn.ErrNotEnrolled) {
		t.Errorf("LoadIdentity = %v, want ErrNotEnrolled", err)
	}
}

// An impostor gets no byte past the handshake, whatever chain it shows.
func TestAnImpostorReceivesNothing(t *testing.T) {
	t.Parallel()
	fleet, other := newCA(t), newCA(t)
	_, stolenKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		signer *ca.Authority
		names  []string
		chain  [][]byte
		// withoutKey replays the certificate with a key that is not its own,
		// as an impostor that copied the hub's public chain would.
		withoutKey bool
	}{
		{"its own CA", other, []string{"127.0.0.1"}, [][]byte{other.Cert.Raw}, false},
		{"the fleet CA in its chain but its own signer", other, []string{"127.0.0.1"}, [][]byte{fleet.Cert.Raw}, false},
		{"no CA in its chain", fleet, []string{"127.0.0.1"}, nil, false},
		{"a fleet certificate for another name", fleet, []string{"elsewhere.example"}, [][]byte{fleet.Cert.Raw}, false},
		{"the hub's own chain without its key", fleet, []string{"127.0.0.1"}, [][]byte{fleet.Cert.Raw}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cert := serverCert(t, tc.signer, tc.names, tc.chain...)
			if tc.withoutKey {
				cert.PrivateKey = stolenKey
			}
			imp := startImpostor(t, cert)
			tok, err := ca.NewToken(fleet.Cert.Raw, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			a := newAgent(t)
			if _, err := a.enroll(t, imp.addr, tok.Reveal()); !errors.Is(err, conn.ErrHubUntrusted) {
				t.Fatalf("Enroll = %v, want ErrHubUntrusted", err)
			}
			if imp.accepted.Load() == 0 {
				t.Fatal("the agent never reached the impostor; the test proves nothing")
			}
			time.Sleep(50 * time.Millisecond)
			if n := imp.received.Load(); n != 0 {
				t.Errorf("the impostor received %d bytes after the handshake", n)
			}
			if lines := a.events(t, "enroll.hub_untrusted"); len(lines) != 1 || lines[0]["peer"] != imp.addr {
				t.Errorf("enroll.hub_untrusted lines %v, want one naming the impostor", lines)
			}
			if lines := a.events(t, "enroll.failed"); len(lines) != 0 {
				t.Errorf("enroll.failed lines %v, want none: the impostor is untrusted, not a failure", lines)
			}
		})
	}
}

// 02g A1 step 4: the agent checks the response before it saves anything.
func TestABadResponseSavesNothing(t *testing.T) {
	t.Parallel()
	other := newCA(t)
	// reissue replaces the certificate with one for the same machine id,
	// signed by signer, for key (nil: the agent's own key).
	reissue := func(res *agentv1.EnrollResponse, signer *ca.Authority, key ed25519.PublicKey) {
		if key == nil {
			b, _ := pem.Decode(res.GetCertPem())
			leaf, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				panic(err)
			}
			pub, ok := leaf.PublicKey.(ed25519.PublicKey)
			if !ok {
				panic("not an Ed25519 key")
			}
			key = pub
		}
		c, err := signer.IssueAgent(res.GetMachineId().GetValue(), key, time.Now(), rand.Reader)
		if err != nil {
			panic(err)
		}
		res.CertPem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.CertDER})
	}
	otherKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	type tamper = func(res *agentv1.EnrollResponse, auth *ca.Authority)
	cases := []struct {
		name   string
		tamper tamper
	}{
		{"certificate from another CA", func(res *agentv1.EnrollResponse, _ *ca.Authority) { reissue(res, other, nil) }},
		{"certificate for another machine", func(res *agentv1.EnrollResponse, _ *ca.Authority) {
			res.MachineId.Value = "01J0000000000000000000ELSE"
		}},
		// The right CA and machine id, so only the key check can catch it.
		{"certificate for another key", func(res *agentv1.EnrollResponse, auth *ca.Authority) { reissue(res, auth, otherKey) }},
		{"two certificates", func(res *agentv1.EnrollResponse, _ *ca.Authority) {
			res.CertPem = append(res.CertPem, res.GetCertPem()...)
		}},
		{"fingerprint differs", func(res *agentv1.EnrollResponse, _ *ca.Authority) {
			res.CaFingerprint = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"CA chain is another CA", func(res *agentv1.EnrollResponse, _ *ca.Authority) {
			res.CaChainPem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Cert.Raw})
		}},
		{"no fleet id", func(res *agentv1.EnrollResponse, _ *ca.Authority) { res.FleetId = "" }},
		{"no salt", func(res *agentv1.EnrollResponse, _ *ca.Authority) { res.AccountSalt = nil }},
		{"no salt version", func(res *agentv1.EnrollResponse, _ *ca.Authority) { res.SaltVersion = 0 }},
		{"no machine id", func(res *agentv1.EnrollResponse, _ *ca.Authority) { res.MachineId = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, tok := startFakeHub(t, &fakeHub{auth: newCA(t), tamper: tc.tamper})
			a := newAgent(t)
			if _, err := a.enroll(t, addr, tok); !errors.Is(err, conn.ErrBadResponse) {
				t.Fatalf("Enroll = %v, want ErrBadResponse", err)
			}
			if _, err := conn.LoadIdentity(a.dir); !errors.Is(err, conn.ErrNotEnrolled) {
				t.Errorf("LoadIdentity = %v, want ErrNotEnrolled", err)
			}
			if got := perm(t, filepath.Join(a.dir, conn.PendingKeyFile)); got != 0o600 {
				t.Errorf("the pending key is mode %04o, want it kept at 0600", got)
			}
		})
	}
}

// The fake hub untampered is accepted, so each case above fails for its
// tamper alone.
func TestTheFakeHubIsAcceptedUntampered(t *testing.T) {
	t.Parallel()
	addr, tok := startFakeHub(t, &fakeHub{auth: newCA(t)})
	a := newAgent(t)
	res, err := a.enroll(t, addr, tok)
	if err != nil {
		t.Fatalf("Enroll = %v", err)
	}
	if res.Identity.FleetID != "fleet-fake" || res.HubAddress != addr {
		t.Errorf("result %+v", res)
	}
}
