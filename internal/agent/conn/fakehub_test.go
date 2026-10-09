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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
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

// impostor is a TLS server that counts every byte a client sends after the
// handshake, as it is read.
type impostor struct {
	addr     string
	received atomic.Int64
	accepted atomic.Int64
}

// counter counts bytes read through it.
type counter struct {
	r io.Reader
	n *atomic.Int64
}

func (c counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
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
				_, _ = io.Copy(io.Discard, counter{r: c, n: &imp.received})
			}()
		}
	}()
	return imp
}

// fakeHub answers Enroll with a correct response that tamper may change,
// unless fail returns an error for that call (numbered from 1).
type fakeHub struct {
	agentv1connect.UnimplementedAgentServiceHandler
	auth   *ca.Authority
	tamper func(res *agentv1.EnrollResponse, auth *ca.Authority)
	fail   func(call int) error
	// hang holds every call until the agent gives up on it.
	hang bool

	mu   sync.Mutex
	reqs []*agentv1.EnrollRequest
}

const fakeMachine = "01J0000000000000000000FAKE"

func (f *fakeHub) Enroll(ctx context.Context, req *connect.Request[agentv1.EnrollRequest],
) (*connect.Response[agentv1.EnrollResponse], error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, proto.CloneOf(req.Msg))
	call := len(f.reqs)
	f.mu.Unlock()
	if f.hang {
		<-ctx.Done()
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	if f.fail != nil {
		if err := f.fail(call); err != nil {
			return nil, err
		}
	}
	csr, err := ca.ParseCSR(req.Msg.GetCsrPem())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	cert, err := f.auth.IssueAgent(fakeMachine, csr.Key, time.Now(), rand.Reader)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sum := sha256.Sum256(f.auth.Cert.Raw)
	res := &agentv1.EnrollResponse{
		MachineId:        &typev1.MachineId{Value: fakeMachine},
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
		f.tamper(res, f.auth)
	}
	return connect.NewResponse(res), nil
}

// requests are the EnrollRequests received, in order.
func (f *fakeHub) requests() []*agentv1.EnrollRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentv1.EnrollRequest(nil), f.reqs...)
}

// hubError is a Connect error with the hub's ErrorDetail.
func hubError(code connect.Code, reason typev1.ReasonCode, retryable bool) error {
	err := connect.NewError(code, errFake)
	if d, derr := connect.NewErrorDetail(&typev1.ErrorDetail{Reason: reason, Retryable: retryable}); derr == nil {
		err.AddDetail(d)
	}
	return err
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "the fake hub says no" }

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
