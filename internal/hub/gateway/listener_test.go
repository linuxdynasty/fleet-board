package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// listenerFixture is a fixture whose certificates are issued at wallNow, so
// the handshake tests do not expire 90 days after t0.
func listenerFixture(t *testing.T) *fixture { t.Helper(); return newFixtureAt(t, wallNow()) }

func TestNewServerRefusesMissingDependencies(t *testing.T) {
	t.Parallel()
	f := listenerFixture(t)
	if _, err := gateway.NewServer(gateway.ServerOptions{Certs: f.certs(t, defaultNames, wallNow), Telemetry: f.tel}); err == nil {
		t.Error("NewServer without a store or a revoked set succeeded")
	}
}

func clientTLS(f *fixture, serverName string) *tls.Config {
	return &tls.Config{RootCAs: f.auth.Pool(), ServerName: serverName, MinVersion: tls.VersionTLS12, Time: wallNow}
}

// dialTLS opens a TLS connection to addr with cfg; the handshake is done.
func dialTLS(t *testing.T, addr string, cfg *tls.Config) (*tls.Conn, error) {
	t.Helper()
	d := tls.Dialer{Config: cfg}
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn, ok := c.(*tls.Conn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("tls.Dialer returned %T", c)
	}
	return conn, nil
}

func TestTheListenerServesTLS13WithTheStoredCertificate(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	f, addr := h.fixture, h.addr
	conn, err := dialTLS(t, addr, clientTLS(f, "127.0.0.1"))
	if err != nil {
		t.Fatalf("TLS dial trusting only the stored CA: %v", err)
	}
	defer func() { _ = conn.Close() }()
	st := conn.ConnectionState()
	if st.Version != tls.VersionTLS13 {
		t.Errorf("negotiated %s, want TLS 1.3", tls.VersionName(st.Version))
	}
	if !bytes.Equal(st.PeerCertificates[0].Raw, f.id.ServerCert) {
		t.Error("the served certificate is not the one in state.db")
	}
	// An enrolling agent holds only the CA's SHA-256 from its token (02g
	// step 2), so the chain carries the CA for it to find by that hash.
	if len(st.PeerCertificates) != 2 || !bytes.Equal(st.PeerCertificates[1].Raw, f.id.CACert) {
		t.Errorf("the handshake sent %d certificates; want the server certificate, then the CA", len(st.PeerCertificates))
	}
}

func TestATLS12OnlyClientIsRefused(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	cfg := clientTLS(h.fixture, "127.0.0.1")
	cfg.MaxVersion = tls.VersionTLS12
	conn, err := dialTLS(t, h.addr, cfg)
	if err == nil {
		_ = conn.Close()
		t.Fatal("a TLS 1.2 client completed a handshake")
	}
	if !strings.Contains(err.Error(), "protocol version") {
		t.Errorf("refused for %q, want the server's protocol version alert", err)
	}
}

// clientCert signs an Ed25519 client certificate with signer.
func clientCert(t *testing.T, signer *ca.Authority) tls.Certificate {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "m-1"},
		NotBefore: wallNow().Add(-time.Hour), NotAfter: wallNow().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer.Cert, pub, signer.Key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// No client certificate is fine (Enroll); one must be signed by the hub CA.
func TestClientCertificatesAreOptionalButMustBeTheHubCAs(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	f, addr := h.fixture, h.addr
	foreign, err := ca.New(wallNow(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		certs []tls.Certificate
		ok    bool
	}{
		{"none", nil, true},
		{"signed by the hub CA", []tls.Certificate{clientCert(t, f.auth)}, true},
		{"signed by another CA", []tls.Certificate{clientCert(t, foreign)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := clientTLS(f, "localhost")
			cfg.Certificates = tc.certs
			conn, err := dialTLS(t, addr, cfg)
			if err == nil {
				// TLS 1.3 reports a refused client certificate on the first read.
				_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, err = conn.Read(make([]byte, 1))
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					err = nil
				}
				_ = conn.Close()
			}
			if tc.ok && err != nil {
				t.Errorf("handshake failed: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("a client certificate from another CA was accepted")
			}
			if !tc.ok && err != nil && !strings.Contains(err.Error(), "remote error") {
				t.Errorf("refused for %q, want the server to refuse the client certificate", err)
			}
		})
	}
}

func TestEveryAgentServiceMethodButEnrollAndRenewCertIsUnimplemented(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	f, addr := h.fixture, h.addr
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS(f, "localhost"), ForceAttemptHTTP2: true}}
	defer httpClient.CloseIdleConnections()
	protocols := []struct {
		name string
		opts []connect.ClientOption
	}{
		{"connect", nil},
		{"grpc", []connect.ClientOption{connect.WithGRPC()}},
	}
	for _, p := range protocols {
		client := agentv1connect.NewAgentServiceClient(httpClient, "https://"+addr, p.opts...)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		calls := map[string]func() error{
			"Connect":    func() error { return bidi(client.Connect(ctx)) },
			"Terminal":   func() error { return bidi(client.Terminal(ctx)) },
			"SessionLog": func() error { return bidi(client.SessionLog(ctx)) },
		}
		for name, call := range calls {
			if code := connect.CodeOf(call()); code != connect.CodeUnimplemented {
				t.Errorf("%s %s: code %v, want unimplemented", p.name, name, code)
			}
		}
		cancel()
	}
}

func bidi[Req, Res any](s *connect.BidiStreamForClient[Req, Res]) error {
	_ = s.CloseRequest()
	_, err := s.Receive()
	_ = s.CloseResponse()
	return err
}

func sample(body, name string, want ...string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		match := true
		for _, w := range want {
			match = match && strings.Contains(line, w)
		}
		if match {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}
