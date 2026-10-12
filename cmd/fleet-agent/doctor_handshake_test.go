package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

func testCAPEM(t *testing.T, a *ca.Authority) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.Cert.Raw}))
}

func testServerCert(t *testing.T, a *ca.Authority, names ...string) tls.Certificate {
	t.Helper()
	sc, err := a.IssueServer(names, time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKCS8PrivateKey(sc.KeyDER)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{sc.CertDER, a.Cert.Raw}, PrivateKey: key}
}

// startHandshakeServer accepts one TLS handshake and reports the
// application bytes the client sent after it, once the client goes away.
func startHandshakeServer(t *testing.T, cert tls.Certificate) (addr string, received <-chan int64) {
	t.Helper()
	var lc net.ListenConfig
	raw, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	done := make(chan int64, 1)
	go func() {
		var n int64
		defer func() { done <- n }()
		c, err := raw.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		s := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
		hctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.HandshakeContext(hctx); err != nil {
			return
		}
		_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 4096)
		for {
			m, err := s.Read(buf)
			n += int64(m)
			if err != nil {
				return
			}
		}
	}()
	return raw.Addr().String(), done
}

func TestDoctorHandshakePassesPinnedCAAndSendsNothing(t *testing.T) {
	t.Parallel()
	a, err := ca.New(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	addr, received := startHandshakeServer(t, testServerCert(t, a, "127.0.0.1"))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := doctorHandshake(ctx, addr, testCAPEM(t, a)); err != nil {
		t.Fatalf("handshake = %v, want nil", err)
	}
	select {
	case n := <-received:
		if n != 0 {
			t.Fatalf("the handshake sent %d application bytes after the TLS handshake", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the test server never saw the client go away")
	}
}

func TestDoctorHandshakeRefusesAnotherCA(t *testing.T) {
	t.Parallel()
	good, err := ca.New(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := ca.New(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := startHandshakeServer(t, testServerCert(t, bad, "127.0.0.1"))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err = doctorHandshake(ctx, addr, testCAPEM(t, good))
	if !errors.Is(err, doctor.ErrHubUntrusted) {
		t.Fatalf("handshake = %v, want ErrHubUntrusted", err)
	}
}

func TestDoctorHandshakeRefusesAClosedPort(t *testing.T) {
	t.Parallel()
	a, err := ca.New(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	raw, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := raw.Addr().String()
	_ = raw.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err = doctorHandshake(ctx, addr, testCAPEM(t, a))
	if err == nil || errors.Is(err, doctor.ErrHubUntrusted) {
		t.Fatalf("handshake = %v, want a transport error, not untrusted", err)
	}
}
