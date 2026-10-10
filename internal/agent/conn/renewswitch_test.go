package conn_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// handshakes records the client certificate of every TLS handshake a hub
// sees, taken or refused, and refuses one that is not valid at the hub's
// TLS clock.
type handshakes struct {
	mu      sync.Mutex
	serials []string
}

func (hs *handshakes) list() []string {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return append([]string(nil), hs.serials...)
}

// verify is a tls.Config.VerifyConnection that records the client
// certificate and checks it against pool at now(); no certificate (an
// Enroll) passes.
func (hs *handshakes) verify(pool *x509.CertPool, now func() time.Time) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return nil
		}
		leaf := cs.PeerCertificates[0]
		hs.mu.Lock()
		hs.serials = append(hs.serials, ca.SerialHex(leaf.SerialNumber))
		hs.mu.Unlock()
		_, err := leaf.Verify(x509.VerifyOptions{
			Roots: pool, CurrentTime: now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		if err != nil {
			return errors.New("the client certificate is not valid here")
		}
		return nil
	}
}

// When the hub refuses the pair an attempt presented (here a renewed pair
// that starts after the hub's clock), the next attempt presents the other
// pair, still inside its life, and connects (backoff unchanged); Connected clears that, so
// after the stream drops the next attempt tries the pair that ends last
// again, then the other.
func TestARefusedPairIsSwappedForTheOtherAtTheNextAttempt(t *testing.T) {
	t.Parallel()
	var hs handshakes
	h := startConnectHubWith(t, func(h *connectHub) {
		pool := x509.NewCertPool()
		pool.AddCert(h.auth.Cert)
		h.tlsCfg.ClientAuth = tls.RequestClientCert
		// The hub's TLS clock is 3 minutes behind: after the CA's start (5
		// minutes back), before the renewed pair's (1 minute back).
		h.tlsCfg.VerifyConnection = hs.verify(pool, func() time.Time { return time.Now().Add(-3 * time.Minute) })
		h.issueAt = func() time.Time { return time.Now().Add(overdue) }
	})
	a := enrolled(t, h)
	enrolledSerial := serialOf(enrolledLeaf(t, a))
	data, lead, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
	writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	want := []string{serialOf(lead), enrolledSerial}
	if got := hs.list(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("handshakes presented %v, want %v: the refused pair, then the other", got, want)
	}
	if got := a.events(t, "renew.pair_switched"); len(got) != 1 || got[0]["to_source"] != "identity" {
		t.Errorf("renew.pair_switched = %v, want one to the enrolled pair", got)
	}
	h.dropLive(t)
	h.connected(t, 2)
	r.running(t)
	want = append(want, serialOf(lead), enrolledSerial)
	if got := hs.list(); len(got) != 4 || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("handshakes presented %v, want %v: Connected clears the switch", got, want)
	}
}

// A stream that connected and then dropped is not a failed attempt: the
// next attempt presents the same pair, not the other (amendment e).
func TestADroppedStreamKeepsThePair(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	data, lead, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
	writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
	startLoop(t, a, nil)
	h.connected(t, 1)
	h.dropLive(t)
	h.connected(t, 2)
	connects := h.calls.of("Connect")
	if connects[0].serial != serialOf(lead) || connects[1].serial != serialOf(lead) {
		t.Errorf("streams presented %s then %s, want the renewed %s twice", connects[0].serial, connects[1].serial, serialOf(lead))
	}
	if got := a.events(t, "renew.pair_switched"); len(got) != 0 {
		t.Errorf("renew.pair_switched = %v after a stream that connected", got)
	}
}

// The pair an attempt presented is forgotten when the next attempt starts:
// an attempt that fails before any handshake (the hub is down) does not
// switch pairs on the strength of an earlier attempt's handshake.
func TestAnAttemptWithNoHandshakeDoesNotSwitch(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	data, lead, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
	writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
	startLoop(t, a, nil)
	h.connected(t, 1)
	h.mu.Lock()
	srv := h.srv
	h.mu.Unlock()
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "an attempt refused at the dial", func() bool {
		for _, e := range a.events(t, "connect.failed") {
			if e["step"] == "dial" {
				return true
			}
		}
		return false
	})
	h.listen(t, h.addr)
	h.connected(t, 2)
	if got := h.calls.of("Connect")[1].serial; got != serialOf(lead) {
		t.Errorf("after the hub came back the agent presented %s, want the renewed %s", got, serialOf(lead))
	}
	if got := a.events(t, "renew.pair_switched"); len(got) != 0 {
		t.Errorf("renew.pair_switched = %v for attempts that never shook hands", got)
	}
}
