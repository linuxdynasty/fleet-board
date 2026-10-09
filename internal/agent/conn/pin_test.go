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
	cases := []struct {
		name   string
		signer *ca.Authority
		names  []string
		chain  [][]byte
	}{
		{"its own CA", other, []string{"127.0.0.1"}, [][]byte{other.Cert.Raw}},
		{"the fleet CA in its chain but its own signer", other, []string{"127.0.0.1"}, [][]byte{fleet.Cert.Raw}},
		{"no CA in its chain", fleet, []string{"127.0.0.1"}, nil},
		{"a fleet certificate for another name", fleet, []string{"elsewhere.example"}, [][]byte{fleet.Cert.Raw}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			imp := startImpostor(t, serverCert(t, tc.signer, tc.names, tc.chain...))
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
		})
	}
}

// 02g A1 step 4: the agent checks the response before it saves anything.
func TestABadResponseSavesNothing(t *testing.T) {
	t.Parallel()
	other := newCA(t)
	reissue := func(res *agentv1.EnrollResponse, signer *ca.Authority) {
		b, _ := pem.Decode(res.GetCertPem())
		leaf, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			panic(err)
		}
		pub, ok := leaf.PublicKey.(ed25519.PublicKey)
		if !ok {
			panic("not an Ed25519 key")
		}
		c, err := signer.IssueAgent(res.GetMachineId().GetValue(), pub, time.Now(), rand.Reader)
		if err != nil {
			panic(err)
		}
		res.CertPem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.CertDER})
	}
	cases := []struct {
		name   string
		tamper func(res *agentv1.EnrollResponse)
	}{
		{"certificate from another CA", func(res *agentv1.EnrollResponse) { reissue(res, other) }},
		{"certificate for another machine", func(res *agentv1.EnrollResponse) { res.MachineId.Value = "01J0000000000000000000ELSE" }},
		{"certificate for another key", func(res *agentv1.EnrollResponse) {
			sc, err := other.IssueServer([]string{"x"}, time.Now(), rand.Reader)
			if err != nil {
				panic(err)
			}
			res.CertPem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sc.CertDER})
		}},
		{"two certificates", func(res *agentv1.EnrollResponse) { res.CertPem = append(res.CertPem, res.GetCertPem()...) }},
		{"fingerprint differs", func(res *agentv1.EnrollResponse) {
			res.CaFingerprint = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"CA chain is another CA", func(res *agentv1.EnrollResponse) {
			res.CaChainPem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Cert.Raw})
		}},
		{"no fleet id", func(res *agentv1.EnrollResponse) { res.FleetId = "" }},
		{"no salt", func(res *agentv1.EnrollResponse) { res.AccountSalt = nil }},
		{"no salt version", func(res *agentv1.EnrollResponse) { res.SaltVersion = 0 }},
		{"no machine id", func(res *agentv1.EnrollResponse) { res.MachineId = nil }},
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
