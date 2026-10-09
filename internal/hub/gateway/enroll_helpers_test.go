package gateway_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// hub is a running agent listener on a real state.db: Enroll writes through
// the store, the revoked set is the real revocation list, and clock is the
// hub's "now" for token expiry and certificate issue.
type hub struct {
	*fixture
	addr    string
	clock   *clock
	revoked *revocation.List
}

// startHub serves a listenerFixture on 127.0.0.1. TLS verification on both
// sides reads wallNow; the hub's enroll clock starts there too.
func startHub(t *testing.T, adjust ...func(*gateway.ServerOptions)) *hub {
	t.Helper()
	return startHubOn(t, listenerFixture(t), adjust...)
}

// startHubOn is startHub on f; the server certificate is re-issued first
// when f's is past two thirds of its life at wallNow.
func startHubOn(t *testing.T, f *fixture, adjust ...func(*gateway.ServerOptions)) *hub {
	t.Helper()
	list, err := revocation.Load(context.Background(), revocation.Options{Dir: f.dir, Store: f.store, Now: wallNow})
	if err != nil {
		t.Fatal(err)
	}
	clk := newClock(wallNow())
	certs := f.certs(t, defaultNames, wallNow)
	if err := certs.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts := gateway.ServerOptions{
		Certs: certs, Store: f.store, Identity: f.id, Revoked: list,
		Telemetry: f.tel, Now: clk.now,
	}
	for _, a := range adjust {
		a(&opts)
	}
	srv, err := gateway.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.TLSConfig.Time = wallNow
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return &hub{fixture: f, addr: ln.Addr().String(), clock: clk, revoked: list}
}

var requestSeq atomic.Int64

// token stores a fresh enroll token for "laptop" valid until notAfter and
// returns its text.
func (h *hub) token(t *testing.T, notAfter time.Time) string {
	t.Helper()
	return h.tokenFor(t, h.id.CACert, notAfter)
}

// tokenFor is token with the CA hash of caDER inside the token text; the
// secret's hash is stored in this hub either way.
func (h *hub) tokenFor(t *testing.T, caDER []byte, notAfter time.Time) string {
	t.Helper()
	tok, err := ca.NewToken(caDER, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reqID := fmt.Sprintf("req-%d", requestSeq.Add(1))
	err = statedb.InsertEnrollToken(context.Background(), h.store, statedb.EnrollToken{
		SecretHash: tok.SecretHash, MachineName: "laptop", Labels: []string{"home"}, NotAfter: notAfter.Truncate(time.Millisecond),
		Created: h.clock.now(), Principal: "local:alice", RequestID: reqID, RequestHash: sha256.Sum256([]byte(reqID)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Reveal()
}

// agentKey is the key an agent generates and the CSR it sends.
type agentKey struct {
	priv ed25519.PrivateKey
	csr  []byte
}

func newAgentKey(t *testing.T) agentKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, priv)
	if err != nil {
		t.Fatal(err)
	}
	return agentKey{priv: priv, csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})}
}

func enrollRequest(token string, k agentKey, install string) *agentv1.EnrollRequest {
	return &agentv1.EnrollRequest{
		Token: token, CsrPem: k.csr, InstallId: install, ProtoMin: 1, ProtoMax: 1,
		Facts: &agentv1.MachineFacts{Hostname: "Laptop.local", Os: "darwin", Arch: "arm64"},
	}
}

// client speaks gRPC over HTTP/2 to the hub, as fleet-agent does, with the
// client certificates in certs (none for an enrolling agent).
func (h *hub) client(t *testing.T, certs ...tls.Certificate) agentv1connect.AgentServiceClient {
	t.Helper()
	cfg := clientTLS(h.fixture, "localhost")
	cfg.Certificates = certs
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}}
	t.Cleanup(hc.CloseIdleConnections)
	return agentv1connect.NewAgentServiceClient(hc, "https://"+h.addr, connect.WithGRPC())
}

func (h *hub) enroll(t *testing.T, req *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := h.client(t).Enroll(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// mustEnroll enrolls and fails the test on an error.
func (h *hub) mustEnroll(t *testing.T, req *agentv1.EnrollRequest) *agentv1.EnrollResponse {
	t.Helper()
	res, err := h.enroll(t, req)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	return res
}

// refusal is the code and ErrorDetail of a Connect error.
type refusal struct {
	code      connect.Code
	reason    typev1.ReasonCode
	retryable bool
}

func refusalOf(t *testing.T, err error) refusal {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a Connect error", err)
	}
	r := refusal{code: ce.Code()}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			t.Fatalf("detail: %v", derr)
		}
		if ed, ok := v.(*typev1.ErrorDetail); ok {
			r.reason, r.retryable = ed.GetReason(), ed.GetRetryable()
		}
	}
	return r
}

// events returns the JSON log lines whose msg is name.
func (h *hub) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(h.logs.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", sc.Text(), err)
		}
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

// tlsCertOf is the client certificate an enrolled agent presents.
func tlsCertOf(t *testing.T, res *agentv1.EnrollResponse, k agentKey) tls.Certificate {
	t.Helper()
	block, _ := pem.Decode(res.GetCertPem())
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("cert_pem is not one CERTIFICATE block: %q", res.GetCertPem())
	}
	return tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: k.priv}
}

// handshake dials addr with cfg and reports whether the server accepted the
// handshake: TLS 1.3 reports a refused client certificate on the first read,
// so a read that times out means accepted.
func handshake(t *testing.T, addr string, cfg *tls.Config) (tls.ConnectionState, error) {
	t.Helper()
	conn, err := dialTLS(t, addr, cfg)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, err = conn.Read(make([]byte, 1))
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		err = nil
	}
	return conn.ConnectionState(), err
}

// tokenUsed reports whether the token's row has a used_time.
func (h *hub) tokenUsed(t *testing.T, tok string) bool {
	t.Helper()
	parsed, err := ca.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	var used sql.NullInt64
	err = h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT used_time FROM enroll_tokens WHERE secret_hash = ?`,
			parsed.SecretHash[:]).Scan(&used)
	})
	if err != nil {
		t.Fatal(err)
	}
	return used.Valid
}

// machineCount is the number of rows in machines.
func (h *hub) machineCount(t *testing.T) int {
	t.Helper()
	var n int
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT count(*) FROM machines`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
