package ca_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

const machineID = "01arz3ndek041061050r3gg28a"

func agentKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestIssueAgentSignsAClientCertificateForTheMachine(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	pub := agentKey(t)
	ac, err := a.IssueAgent(machineID, pub, t0, rand.Reader)
	if err != nil {
		t.Fatalf("IssueAgent: %v", err)
	}
	c, err := x509.ParseCertificate(ac.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(x509.VerifyOptions{
		Roots: a.Pool(), CurrentTime: t0, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("does not verify as a client certificate of the CA: %v", err)
	}
	if _, err := c.Verify(x509.VerifyOptions{
		Roots: a.Pool(), CurrentTime: t0, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err == nil {
		t.Error("an agent certificate verifies as a server certificate")
	}
	if c.Subject.CommonName != machineID {
		t.Errorf("CN %q, want %q", c.Subject.CommonName, machineID)
	}
	if len(c.URIs) != 1 || c.URIs[0].String() != "fleet://machine/"+machineID {
		t.Errorf("URI SANs %v, want [fleet://machine/%s]", c.URIs, machineID)
	}
	if len(c.DNSNames)+len(c.IPAddresses)+len(c.EmailAddresses) != 0 {
		t.Error("the certificate has SANs besides the machine URI")
	}
	if c.IsCA || c.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("IsCA %v, KeyUsage %v; want a leaf with digital signature only", c.IsCA, c.KeyUsage)
	}
	if len(c.ExtKeyUsage) != 1 || c.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("ExtKeyUsage %v, want client auth only", c.ExtKeyUsage)
	}
	if got, ok := c.PublicKey.(ed25519.PublicKey); !ok || !got.Equal(pub) {
		t.Error("the certificate does not carry the CSR's key")
	}
	wantStart := time.Date(2026, 10, 5, 11, 55, 0, 0, time.UTC)
	if !c.NotBefore.Equal(wantStart) || !c.NotAfter.Equal(wantStart.Add(90*24*time.Hour)) {
		t.Errorf("valid %v to %v, want %v plus 90 days", c.NotBefore, c.NotAfter, wantStart)
	}
	if !ac.NotAfter.Equal(c.NotAfter) || ac.Serial.Cmp(c.SerialNumber) != 0 {
		t.Error("AgentCert.NotAfter or Serial differs from the certificate")
	}
	if c.SerialNumber.Sign() <= 0 || c.SerialNumber.Cmp(new(big.Int).Lsh(big.NewInt(1), 128)) > 0 {
		t.Errorf("serial %v is not a positive 128-bit number", c.SerialNumber)
	}
}

func TestIssueAgentStopsAtTheCAsExpiry(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	late := a.Cert.NotAfter.Add(-30 * 24 * time.Hour)
	ac, err := a.IssueAgent(machineID, agentKey(t), late, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !ac.NotAfter.Equal(a.Cert.NotAfter) {
		t.Errorf("NotAfter %v, want the CA's %v", ac.NotAfter, a.Cert.NotAfter)
	}
}

func TestIssueAgentRefusesAnEmptyMachineIDOrKey(t *testing.T) {
	t.Parallel()
	a := newCA(t)
	if _, err := a.IssueAgent("", agentKey(t), t0, rand.Reader); err == nil {
		t.Error("issued a certificate for an empty machine id")
	}
	if _, err := a.IssueAgent(machineID, nil, t0, rand.Reader); err == nil {
		t.Error("issued a certificate for no key")
	}
}

func TestSerialHexIsLowercaseHexWithoutPadding(t *testing.T) {
	t.Parallel()
	n, ok := new(big.Int).SetString("0aBCdef", 16)
	if !ok {
		t.Fatal("bad literal")
	}
	if got := ca.SerialHex(n); got != "abcdef" {
		t.Errorf("SerialHex = %q, want abcdef", got)
	}
}
