package daemon_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// writeRevoked writes <state>/revoked-serials with body and mode.
func (h *hub) writeRevoked(t *testing.T, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(h.state, "revoked-serials")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// agentCert signs a client certificate for machineID with the stored CA,
// as Enroll would.
func agentCert(t *testing.T, h *hub, machineID string) (tls.Certificate, ca.AgentCert) {
	t.Helper()
	id := h.storedIdentity(t)
	auth, err := ca.Parse(id.CACert, id.CAKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.IssueAgent(machineID, pub, time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{c.CertDER}, PrivateKey: priv}, c
}

// accepted dials the agent listener with cert and reports the server's
// verdict: TLS 1.3 reports a refused client certificate on the first read.
func accepted(t *testing.T, h *hub, addr net.Addr, cert tls.Certificate) error {
	t.Helper()
	cfg := &tls.Config{
		RootCAs: caPool(t, h.storedIdentity(t)), ServerName: "localhost", MinVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	d := tls.Dialer{Config: cfg}
	c, err := d.DialContext(t.Context(), "tcp", "127.0.0.1:"+port(addr))
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, err = c.Read(make([]byte, 1))
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return nil
	}
	return err
}

// A serial in revoked-serials when fleetd starts is refused at the first
// handshake: the list is loaded before the agent listener binds. The file's
// serial state.db does not know stays in the file; another serial passes.
func TestRunLoadsRevocationsBeforeTheAgentListenerServes(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	revoked, rc := agentCert(t, h, "01jrevoked000000000000000a")
	live, _ := agentCert(t, h, "01jlive0000000000000000000")
	line := ca.SerialHex(rc.Serial) + " " + rc.NotAfter.UTC().Format(time.RFC3339) + "\n"
	h.writeRevoked(t, "# fleet-board revoked-serials v1\n"+line, 0o600)
	r := h.start(t, h.options())

	if err := accepted(t, h, r.addrs.Agent, revoked); err == nil || !strings.Contains(err.Error(), "remote error") {
		t.Errorf("a serial in revoked-serials: %v, want the server to refuse it", err)
	}
	if err := accepted(t, h, r.addrs.Agent, live); err != nil {
		t.Errorf("a live serial was refused: %v", err)
	}
	if got := sample(h.scrape(t), "fleet_hub_auth_failures_total", `reason="revoked"`); got != 1 {
		t.Errorf("fleet_hub_auth_failures_total{reason=revoked} = %v, want 1", got)
	}
	r.stop(t)
	b, err := os.ReadFile(filepath.Join(h.state, "revoked-serials"))
	if err != nil || !strings.Contains(string(b), line) {
		t.Errorf("revoked-serials after the run: %q (%v), want it to keep %q", b, err, line)
	}
}

// End to end through fleetd run: a token from the admin socket enrolls a
// machine over the agent listener, and the listener accepts its certificate.
func TestRunEnrollsAMachineWithATokenFromTheAdminSocket(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	r := h.start(t, h.options())
	res, cert := enrollOverRun(t, h, r)
	if res.GetMachineName() != "laptop" || res.GetFleetId() == "" {
		t.Errorf("machine_name %q, fleet_id %q", res.GetMachineName(), res.GetFleetId())
	}
	if err := accepted(t, h, r.addrs.Agent, cert); err != nil {
		t.Errorf("the listener refused the enrolled certificate: %v", err)
	}
	if got := sample(h.scrape(t), "fleet_hub_enroll_total", `result="enrolled"`); got != 1 {
		t.Errorf("fleet_hub_enroll_total{result=enrolled} = %v, want 1", got)
	}
}

// enrollOverRun enrolls machine "laptop" with a token from the admin
// socket and returns the response and the client certificate.
func enrollOverRun(t *testing.T, h *hub, r *running) (*agentv1.EnrollResponse, tls.Certificate) {
	t.Helper()
	tok, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{Machine: "laptop"})
	if err != nil {
		t.Fatalf("EnrollToken: %v", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, priv)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agentClient(t, h, r).Enroll(context.Background(), connect.NewRequest(&agentv1.EnrollRequest{
		Token: tok.GetToken(), CsrPem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}),
		InstallId: "install-1", ProtoMin: 1, ProtoMax: 1, Facts: &agentv1.MachineFacts{Hostname: "laptop"},
	}))
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	block, _ := pem.Decode(res.Msg.GetCertPem())
	if block == nil {
		t.Fatal("no certificate in the response")
	}
	return res.Msg, tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: priv}
}

// agentClient speaks gRPC to r's agent listener, presenting certs.
func agentClient(t *testing.T, h *hub, r *running, certs ...tls.Certificate) agentv1connect.AgentServiceClient {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: caPool(t, h.storedIdentity(t)), ServerName: "localhost", MinVersion: tls.VersionTLS13, Certificates: certs,
		},
		ForceAttemptHTTP2: true,
	}}
	t.Cleanup(hc.CloseIdleConnections)
	return agentv1connect.NewAgentServiceClient(hc, "https://127.0.0.1:"+port(r.addrs.Agent), connect.WithGRPC())
}
