package conn_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
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
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// connectHub is a hub fake for the connection loop: fakeHub's Enroll, and
// Connect with the hub's rules for epoch (max(stored, last_epoch) + 1),
// hub_last_event_seq (the highest snapshot seq stored) and duplicate
// identity (a newer stream displaces the live one with
// Bye{DUPLICATE_IDENTITY}). Connect needs a client certificate from the
// fake CA.
type connectHub struct {
	*fakeHub
	addr, token string
	handler     http.Handler
	tlsCfg      *tls.Config
	open        atomic.Int64 // TCP connections open now
	calls       callLog      // every request: method, connection, client certificate

	mu   sync.Mutex
	srv  *http.Server
	held *conntest.Held // the socket srv serves on
	done chan struct{}  // closed when srv's Serve has returned
	// refuse may fail stream n instead of HelloAck.
	refuse func(n int) error
	// cutAfterAck may fail stream n right after its HelloAck.
	cutAfterAck func(n int) error
	// deaf stops reading every stream after HelloAck; HTTP/2 pings are
	// still answered.
	deaf bool
	// fleetID is HelloAck.fleet_id; the enrolled one by default.
	fleetID string
	// onHello may answer stream n with a Bye instead of HelloAck.
	onHello func(n int, h *agentv1.Hello) *agentv1.Bye
	// tamperAck may change stream n's HelloAck.
	tamperAck func(n int, ack *agentv1.HelloAck)
	// ackResults answers each CommandResult with a ResultAck.
	ackResults bool
	// readGate, when set, holds every stream's next read until it closes.
	readGate chan struct{}
	// renew is how RenewCert answers (renewfake_test.go).
	renew renewFake
	// hubClock is the hub's clock: HelloAck.hub_time and the issue time of
	// a renewed certificate. nil is time.Now.
	hubClock func() time.Time
	epoch    uint64
	lastSeq  uint64
	live     *liveStream
	streams  []*streamRecord
}

// streamRecord is what one Connect stream received, in order.
type streamRecord struct {
	at    time.Time
	hello *agentv1.Hello
	msgs  []*agentv1.ConnectRequest
	times []time.Time // when each of msgs was read
	ended bool
}

type liveStream struct {
	out       chan *agentv1.ConnectResponse
	drop      chan struct{}
	displaced chan struct{}
	// last is sent, and then the stream ends without the agent's help.
	last chan *agentv1.ConnectResponse
}

func startConnectHub(t *testing.T) *connectHub {
	t.Helper()
	return startConnectHubWith(t, nil)
}

// startConnectHubWith is startConnectHub after set adjusts the fake (its
// TLS clock, its enroll issue time) before it serves.
func startConnectHubWith(t *testing.T, set func(h *connectHub)) *connectHub {
	t.Helper()
	h := newConnectHub(t)
	if set != nil {
		set(h)
	}
	h.listen(t, "127.0.0.1:0")
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		_ = h.srv.Close()
		h.held.Close()
	})
	return h
}

// newConnectHub builds the fake without serving it, so its handler can
// also serve another listener's Connect (see renewhub_test.go).
func newConnectHub(t *testing.T) *connectHub {
	t.Helper()
	h := &connectHub{fakeHub: &fakeHub{auth: newCA(t)}, fleetID: "fleet-fake"}
	path, handler := agentv1connect.NewAgentServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Connect") && (r.TLS == nil || len(r.TLS.PeerCertificates) == 0) {
			http.Error(w, "a client certificate is required", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	pool := x509.NewCertPool()
	pool.AddCert(h.auth.Cert)
	h.handler = mux
	h.tlsCfg = &tls.Config{
		Certificates: []tls.Certificate{serverCert(t, h.auth, []string{"127.0.0.1"}, h.auth.Cert.Raw)},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
	}
	tok, err := ca.NewToken(h.auth.Cert.Raw, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h.token = tok.Reveal()
	return h
}

// bindWait is how long listen retries a port another process holds.
const bindWait = 30 * time.Second

// listen serves the hub on addr and records the address. A fixed addr (the
// hub coming back after a stop) is bound with retries, for it may be in
// use for a moment; a restart that keeps the port uses swap instead.
func (h *connectHub) listen(t *testing.T, addr string) {
	t.Helper()
	held := conntest.Hold(t, conntest.BindRetry(t, addr, bindWait))
	h.serveOn(held)
	if h.addr == "" {
		h.addr = held.Addr().String()
	}
}

// serveOn starts a new server on a view of held.
func (h *connectHub) serveOn(held *conntest.Held) {
	srv := &http.Server{
		Handler: h.calls.record(h.handler), ReadHeaderTimeout: 5 * time.Second, TLSConfig: h.tlsCfg.Clone(),
		ConnState: func(_ net.Conn, s http.ConnState) {
			switch s {
			case http.StateNew:
				h.open.Add(1)
			case http.StateClosed, http.StateHijacked:
				h.open.Add(-1)
			default:
			}
		},
	}
	done := make(chan struct{})
	ln := held.View()
	go func() {
		defer close(done)
		_ = srv.ServeTLS(ln, "", "")
	}()
	h.mu.Lock()
	h.srv, h.held, h.done = srv, held, done
	h.mu.Unlock()
}

// swap replaces the server with a new one on the same socket, which never
// closes, so no other process can take the port in between (conntest/held.go).
// Connections that arrive meanwhile wait in the accept queue.
func (h *connectHub) swap(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	srv, held, done := h.srv, h.held, h.done
	h.mu.Unlock()
	held.KeepOpen(true)
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	held.KeepOpen(false)
	h.serveOn(held)
}

// restart says Bye{HUB_RESTART} on the live stream, closes the hub and
// serves it again on the same port, keeping its epoch and seqs.
func (h *connectHub) restart(t *testing.T) {
	t.Helper()
	n := h.count()
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{
		Bye: bye(agentv1.ByeReason_BYE_REASON_HUB_RESTART),
	}})
	waitFor(t, 5*time.Second, "the Bye to end the stream", func() bool { return h.stream(n).ended })
	h.mu.Lock()
	h.live = nil
	h.mu.Unlock()
	h.swap(t)
}

func (h *connectHub) Connect(ctx context.Context,
	bs *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse],
) error {
	first, err := bs.Receive()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("hello must come first"))
	}
	rec := &streamRecord{at: time.Now(), hello: proto.CloneOf(hello)}
	defer h.end(rec)
	h.mu.Lock()
	h.streams = append(h.streams, rec)
	n := len(h.streams)
	var bye *agentv1.Bye
	if h.onHello != nil {
		bye = h.onHello(n, hello)
	}
	if bye != nil {
		h.mu.Unlock()
		return bs.Send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: bye}})
	}
	if h.refuse != nil {
		if err := h.refuse(n); err != nil {
			h.mu.Unlock()
			return err
		}
	}
	var cut error
	if h.cutAfterAck != nil {
		cut = h.cutAfterAck(n)
	}
	deaf := h.deaf
	ls := &liveStream{
		out: make(chan *agentv1.ConnectResponse, 16), drop: make(chan struct{}),
		displaced: make(chan struct{}), last: make(chan *agentv1.ConnectResponse, 1),
	}
	old := h.live
	h.live = ls
	h.epoch = max(h.epoch, hello.GetLastEpoch()) + 1
	ack := &agentv1.HelloAck{
		HubVersion: "fake", HubTime: timestamppb.New(h.hubNowLocked()), Epoch: h.epoch, ProtoVersion: protoversion.Max,
		HubLastEventSeq: h.lastSeq, MaxMessageBytes: 1 << 20, HubInstanceId: "hub-fake", FleetId: h.fleetID,
	}
	if h.tamperAck != nil {
		h.tamperAck(n, ack)
	}
	h.mu.Unlock()
	if old != nil {
		close(old.displaced)
	}
	if err := bs.Send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_HelloAck{HelloAck: ack}}); err != nil {
		return err
	}
	switch {
	case cut != nil:
		return cut
	case deaf:
		return h.deafServe(ctx, bs, ls)
	}
	return h.serve(ctx, bs, ls, rec)
}

// deafServe sends what is pushed and never reads, so the agent's sends
// stall once the HTTP/2 flow-control window is full.
func (h *connectHub) deafServe(ctx context.Context, bs *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse],
	ls *liveStream,
) error {
	for {
		select {
		case r := <-ls.out:
			if err := bs.Send(r); err != nil {
				return err
			}
		case <-ls.drop:
			return connect.NewError(connect.CodeUnavailable, errors.New("the fake hub dropped the stream"))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *connectHub) serve(ctx context.Context, bs *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse],
	ls *liveStream, rec *streamRecord,
) error {
	in := make(chan *agentv1.ConnectRequest)
	errc := make(chan error, 1)
	go func() {
		for {
			h.mu.Lock()
			g := h.readGate
			h.mu.Unlock()
			if g != nil {
				select {
				case <-g:
				case <-ctx.Done():
					return
				}
			}
			m, err := bs.Receive()
			if err != nil {
				errc <- err
				return
			}
			select {
			case in <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case m := <-in:
			if ack := h.record(rec, m); ack != nil {
				if err := bs.Send(ack); err != nil {
					return err
				}
			}
		case err := <-errc:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case r := <-ls.out:
			if err := bs.Send(r); err != nil {
				return err
			}
		case <-ls.displaced:
			return bs.Send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{
				Bye: &agentv1.Bye{Reason: agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY},
			}})
		case r := <-ls.last:
			if err := bs.Send(r); err != nil {
				return err
			}
			return connect.NewError(connect.CodeUnavailable, errors.New("the fake hub ended the stream"))
		case <-ls.drop:
			return connect.NewError(connect.CodeUnavailable, errors.New("the fake hub dropped the stream"))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// record keeps m and returns the ResultAck to send, if any.
func (h *connectHub) record(rec *streamRecord, m *agentv1.ConnectRequest) *agentv1.ConnectResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec.msgs = append(rec.msgs, proto.CloneOf(m))
	rec.times = append(rec.times, time.Now())
	if s := m.GetSnapshot(); s != nil {
		h.lastSeq = max(h.lastSeq, s.GetSeq())
	}
	if r := m.GetCommandResult(); r != nil && h.ackResults {
		return resultAck(r.GetCommandId().GetValue())
	}
	return nil
}

func resultAck(ids ...string) *agentv1.ConnectResponse {
	ack := &agentv1.ResultAck{}
	for _, id := range ids {
		ack.CommandIds = append(ack.CommandIds, &typev1.CommandId{Value: id})
	}
	return &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_ResultAck{ResultAck: ack}}
}

func bye(r agentv1.ByeReason) *agentv1.Bye { return &agentv1.Bye{Reason: r} }

func (h *connectHub) end(rec *streamRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec.ended = true
}

// set runs fn under the hub's lock, to change its behavior.
func (h *connectHub) set(fn func(h *connectHub)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

// push sends r on the live stream.
func (h *connectHub) push(t *testing.T, r *agentv1.ConnectResponse) {
	t.Helper()
	h.mu.Lock()
	ls := h.live
	h.mu.Unlock()
	if ls == nil {
		t.Fatal("no live stream")
	}
	ls.out <- r
}

// dropLive cuts the live stream without a Bye.
func (h *connectHub) dropLive(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	ls := h.live
	h.live = nil
	h.mu.Unlock()
	if ls == nil {
		t.Fatal("no live stream")
	}
	close(ls.drop)
}

// count is the number of streams opened so far.
func (h *connectHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.streams)
}

// stream returns a copy of stream n (from 1).
func (h *connectHub) stream(n int) streamRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.streams[n-1]
	return streamRecord{
		at: r.at, hello: r.hello, msgs: append([]*agentv1.ConnectRequest(nil), r.msgs...),
		times: append([]time.Time(nil), r.times...), ended: r.ended,
	}
}

// snapshots are the snapshots stream n received.
func (h *connectHub) snapshots(n int) []*agentv1.Snapshot {
	var out []*agentv1.Snapshot
	for _, m := range h.stream(n).msgs {
		if s := m.GetSnapshot(); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// connected waits until stream n has its snapshot.
func (h *connectHub) connected(t *testing.T, n int) {
	t.Helper()
	waitFor(t, 10*time.Second, "a snapshot on stream "+strconv.Itoa(n), func() bool {
		return h.count() >= n && len(h.snapshots(n)) > 0
	})
}

// waitFor polls cond until it holds, or fails the test after limit.
func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timed out after %v waiting for %s", limit, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
