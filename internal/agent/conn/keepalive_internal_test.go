package conn

import (
	"crypto/tls"
	"testing"
	"time"
)

// ADR 0004: a zero Keepalive pings after 10 s of silence and gives up 20 s
// later, and the link puts those values on its HTTP/2 transport. The
// keepalive tests in keepalive_test.go prove the transport acts on them.
func TestAZeroKeepaliveTakesTheDefaults(t *testing.T) {
	t.Parallel()
	ka := (Keepalive{}).withDefaults()
	if want := (Keepalive{Ping: 10 * time.Second, Timeout: 20 * time.Second}); ka != want {
		t.Errorf("Keepalive{} = %+v, want %+v", ka, want)
	}
	k, err := newLink("127.0.0.1:1", [32]byte{}, func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return &tls.Certificate{}, nil
	}, ka, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer k.close()
	if h2 := k.tr.HTTP2; h2 == nil || h2.SendPingTimeout != 10*time.Second || h2.PingTimeout != 20*time.Second {
		t.Errorf("the link's HTTP/2 config = %+v, want a ping after 10s and a 20s timeout", h2)
	}
}
