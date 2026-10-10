package conn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// The link reads the client certificate at each handshake, not once when
// it is built: after the pair is swapped, the next connection the same
// link dials presents the new certificate, while a connection already open
// keeps the one it shook hands with.
func TestALinkPresentsThePairInUseAtEachHandshake(t *testing.T) {
	t.Parallel()
	auth, err := ca.New(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := auth.IssueServer([]string{"127.0.0.1"}, time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	skey, err := x509.ParsePKCS8PrivateKey(sc.KeyDER)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var serials []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		serials = append(serials, r.TLS.PeerCertificates[0].SerialNumber.Text(16))
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{sc.CertDER, auth.Cert.Raw}, PrivateKey: skey}},
		ClientAuth:   tls.RequireAnyClientCert,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pairs := [2]*tls.Certificate{agentPair(t, auth), agentPair(t, auth)}
	var use atomic.Pointer[tls.Certificate]
	use.Store(pairs[0])
	k, err := newLink(srv.Listener.Addr().String(), sha256.Sum256(auth.Cert.Raw),
		func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return use.Load(), nil }, Keepalive{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.close)
	client := &http.Client{Transport: k.tr}
	get := func() {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}
	get()
	use.Store(pairs[1])
	get() // the open connection keeps its handshake
	k.tr.CloseIdleConnections()
	get() // a new handshake on the same link
	want := []string{serialHex(pairs[0]), serialHex(pairs[0]), serialHex(pairs[1])}
	mu.Lock()
	defer mu.Unlock()
	if len(serials) != 3 || serials[0] != want[0] || serials[1] != want[1] || serials[2] != want[2] {
		t.Errorf("serials presented %v, want %v", serials, want)
	}
}

func agentPair(t *testing.T, auth *ca.Authority) *tls.Certificate {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.IssueAgent("m", pub, time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{c.CertDER}, PrivateKey: key}
}

func serialHex(c *tls.Certificate) string {
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		panic(err)
	}
	return leaf.SerialNumber.Text(16)
}
