package conn_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// newCA makes a fleet CA for a test.
func newCA(t *testing.T) *ca.Authority {
	t.Helper()
	a, err := ca.New(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// serverCert is a TLS certificate signed by signer for names, sent with the
// given extra chain certificates.
func serverCert(t *testing.T, signer *ca.Authority, names []string, chain ...[]byte) tls.Certificate {
	t.Helper()
	sc, err := signer.IssueServer(names, time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKCS8PrivateKey(sc.KeyDER)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: append([][]byte{sc.CertDER}, chain...), PrivateKey: key}
}

// impostor is a TLS server that counts the bytes a client sends after the
// handshake.
type impostor struct {
	addr     string
	received atomic.Int64
	accepted atomic.Int64
}

func startImpostor(t *testing.T, cert tls.Certificate) *impostor {
	t.Helper()
	var lc net.ListenConfig
	raw, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}})
	t.Cleanup(func() { _ = ln.Close() })
	imp := &impostor{addr: raw.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			imp.accepted.Add(1)
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				n, _ := io.Copy(io.Discard, c)
				imp.received.Add(n)
			}()
		}
	}()
	return imp
}

// fakeHub answers Enroll with a correct response that tamper may change.
type fakeHub struct {
	agentv1connect.UnimplementedAgentServiceHandler
	auth   *ca.Authority
	tamper func(res *agentv1.EnrollResponse)
}

func (f *fakeHub) Enroll(_ context.Context, req *connect.Request[agentv1.EnrollRequest],
) (*connect.Response[agentv1.EnrollResponse], error) {
	csr, err := ca.ParseCSR(req.Msg.GetCsrPem())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	const id = "01J0000000000000000000FAKE"
	cert, err := f.auth.IssueAgent(id, csr.Key, time.Now(), rand.Reader)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sum := sha256.Sum256(f.auth.Cert.Raw)
	res := &agentv1.EnrollResponse{
		MachineId:        &typev1.MachineId{Value: id},
		CertPem:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.CertDER}),
		CaChainPem:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.auth.Cert.Raw}),
		CaFingerprint:    hex.EncodeToString(sum[:]),
		CertNotAfterTime: timestamppb.New(cert.NotAfter),
		AccountSalt:      make([]byte, 32),
		SaltVersion:      1,
		MachineName:      "laptop",
		FleetId:          "fleet-fake",
	}
	if f.tamper != nil {
		f.tamper(res)
	}
	return connect.NewResponse(res), nil
}

// startFakeHub serves f over TLS with the chain [server, CA] on 127.0.0.1,
// and returns its address and a token that pins f's CA.
func startFakeHub(t *testing.T, f *fakeHub) (addr, token string) {
	t.Helper()
	path, handler := agentv1connect.NewAgentServiceHandler(f)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{serverCert(t, f.auth, []string{"127.0.0.1"}, f.auth.Cert.Raw)},
			MinVersion:   tls.VersionTLS13,
		},
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	tok, err := ca.NewToken(f.auth.Cert.Raw, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return ln.Addr().String(), tok.Reveal()
}
