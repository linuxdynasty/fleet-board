package conn_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// renewFake is how connectHub answers RenewCert; it is read and changed
// under connectHub.mu.
type renewFake struct {
	// tamper may change a correct answer.
	tamper func(res *agentv1.RenewCertResponse, auth *ca.Authority)
	// fail may fail call n (from 1) instead of answering.
	fail func(n int) error
	// hold, when set, holds every call until it opens or the call ends.
	hold *gate
	// issueAt is the issue time of the certificate; nil is an hour past
	// the hub's clock, so the new certificate ends later than the enrolled
	// one and is not due by the hub's time.
	issueAt func() time.Time
	// keys are the CSR keys of every call, in order.
	keys []ed25519.PublicKey
}

// RenewCert signs a certificate for the CSR's key with the fake CA, unless
// renew.fail says otherwise, then lets renew.tamper change it.
func (h *connectHub) RenewCert(ctx context.Context, req *connect.Request[agentv1.RenewCertRequest],
) (*connect.Response[agentv1.RenewCertResponse], error) {
	csr, err := ca.ParseCSR(req.Msg.GetCsrPem())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	h.mu.Lock()
	h.renew.keys = append(h.renew.keys, csr.Key)
	n := len(h.renew.keys)
	f := h.renew
	at := h.hubNowLocked().Add(time.Hour)
	h.mu.Unlock()
	if f.hold != nil {
		if err := f.hold.wait(ctx); err != nil {
			return nil, connect.NewError(connect.CodeCanceled, err)
		}
	}
	if f.fail != nil {
		if err := f.fail(n); err != nil {
			return nil, err
		}
	}
	if f.issueAt != nil {
		at = f.issueAt()
	}
	cert, err := h.auth.IssueAgent(fakeMachine, csr.Key, at, rand.Reader)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.RenewCertResponse{
		CertPem:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.CertDER}),
		CaChainPem:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.auth.Cert.Raw}),
		CertNotAfterTime: timestamppb.New(cert.NotAfter),
	}
	if f.tamper != nil {
		f.tamper(res, h.auth)
	}
	return connect.NewResponse(res), nil
}

// hubNowLocked is the hub's clock; h.mu is held.
func (h *connectHub) hubNowLocked() time.Time {
	if h.hubClock != nil {
		return h.hubClock()
	}
	return time.Now()
}

// dueHub makes h's clock (HelloAck.hub_time) 61 days ahead, past two
// thirds of a certificate enrolled now, so the agent renews at once.
func dueHub(h *connectHub) { h.set(func(h *connectHub) { h.hubClock = renewDueClock }) }

// countChecks installs a renew hook that counts renewal checks.
func countChecks(o *conn.LoopOptions) *atomic.Int64 {
	var n atomic.Int64
	conn.SetRenewHook(o, func(step string) bool {
		if step == "check" {
			n.Add(1)
		}
		return false
	})
	return &n
}

// renewKeys are the CSR keys RenewCert received, in order.
func (h *connectHub) renewKeys() []ed25519.PublicKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ed25519.PublicKey(nil), h.renew.keys...)
}

// hubCall is one request a hub served.
type hubCall struct {
	method string // the RPC name, such as "Connect" or "RenewCert"
	remote string // the client's address: one per TCP connection
	serial string // the client certificate's serial (ca.SerialHex), or ""
}

// callLog records every request a hub serves.
type callLog struct {
	mu    sync.Mutex
	calls []hubCall
}

// record wraps next so each request is logged before it is served.
func (c *callLog) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := hubCall{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], remote: r.RemoteAddr}
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			call.serial = ca.SerialHex(r.TLS.PeerCertificates[0].SerialNumber)
		}
		c.mu.Lock()
		c.calls = append(c.calls, call)
		c.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// of returns the calls of method, in order.
func (c *callLog) of(method string) []hubCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []hubCall
	for _, call := range c.calls {
		if call.method == method {
			out = append(out, call)
		}
	}
	return out
}

// skewClock is the wall clock moved by an offset a test changes.
type skewClock struct{ off atomic.Int64 }

func (c *skewClock) now() time.Time { return time.Now().Add(time.Duration(c.off.Load())) }

func (c *skewClock) set(d time.Duration) { c.off.Store(int64(d)) }

// since is time.Since moved by the offset: as conn.SetElapsed, it moves
// the agent's measure of hub time on by the offset.
func (c *skewClock) since(t time.Time) time.Duration {
	return time.Since(t) + time.Duration(c.off.Load())
}

// errNoRenew is a RenewCert refusal a fake returns.
var errNoRenew = errors.New("the fake hub does not renew")
