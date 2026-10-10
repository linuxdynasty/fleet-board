package gateway

import (
	"bytes"
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// stubWire records what the writer sends and the write deadlines it sets;
// block, when set, holds every Send until a deadline is set.
type stubWire struct {
	mu        sync.Mutex
	sent      []*agentv1.ConnectResponse
	deadlines []time.Time
	block     chan struct{}
}

func (w *stubWire) Send(m *agentv1.ConnectResponse) error {
	w.mu.Lock()
	block := w.block
	w.mu.Unlock()
	if block != nil {
		<-block
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, m)
	return nil
}

func (w *stubWire) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, t)
	if w.block != nil {
		close(w.block)
		w.block = nil
	}
	return nil
}

func ack(epoch uint64) *agentv1.ConnectResponse {
	return &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_HelloAck{HelloAck: &agentv1.HelloAck{Epoch: epoch}}}
}

// When the stream ends with a Bye, what was queued goes first, in order,
// then the Bye, all under one write deadline of byeWait.
func TestWriterFlushesWhatIsQueuedThenSaysBye(t *testing.T) {
	t.Parallel()
	for range 20 { // the writer's select sees the queue and the end together
		s := newStream("m1", "boot", "p", 1)
		w := &stubWire{}
		for e := range uint64(3) {
			s.send(ack(e + 1))
		}
		s.end(agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY, statedb.EndDisplaced)
		before := time.Now()
		done := make(chan struct{})
		s.write(w, w, done)
		<-done
		if len(w.sent) != 4 || w.sent[0].GetHelloAck().GetEpoch() != 1 || w.sent[2].GetHelloAck().GetEpoch() != 3 ||
			w.sent[3].GetBye().GetReason() != agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY {
			t.Fatalf("sent %v, want the three queued messages in order, then the Bye", w.sent)
		}
		if len(w.deadlines) != 1 || w.deadlines[0].Before(before.Add(byeWait)) || w.deadlines[0].After(time.Now().Add(byeWait)) {
			t.Fatalf("write deadlines %v, want one at about now + %v", w.deadlines, byeWait)
		}
	}
}

// A stream that ends with no Bye sends nothing more and sets no deadline.
func TestWriterSendsNothingAfterAnEndWithoutBye(t *testing.T) {
	t.Parallel()
	s := newStream("m1", "boot", "p", 1)
	w := &stubWire{}
	s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndAgentBye)
	done := make(chan struct{})
	s.write(w, w, done)
	if len(w.sent) != 0 || len(w.deadlines) != 0 {
		t.Errorf("sent %v, deadlines %v; want none", w.sent, w.deadlines)
	}
}

// stop returns at once for a writer that is done, and cuts a writer still
// blocked after the wait with a write deadline of now.
func TestStopCutsAWriterBlockedPastTheWait(t *testing.T) {
	t.Parallel()
	a := newTestDeps(t)
	done := make(chan struct{})
	close(done)
	w := &stubWire{}
	if a.stop(w, done, time.Hour) {
		t.Error("stop reported a cut for a finished writer")
	}
	if len(w.deadlines) != 0 {
		t.Errorf("a finished writer got deadlines %v", w.deadlines)
	}
	blocked := &stubWire{block: make(chan struct{})}
	wrote := make(chan struct{})
	go func() {
		_ = blocked.Send(ack(1))
		close(wrote)
	}()
	before := time.Now()
	if !a.stop(blocked, wrote, 20*time.Millisecond) {
		t.Error("stop did not report the cut")
	}
	blocked.mu.Lock()
	defer blocked.mu.Unlock()
	if len(blocked.deadlines) != 1 || blocked.deadlines[0].Before(before.Add(20*time.Millisecond)) {
		t.Errorf("deadlines %v, want one cut at least 20 ms after the start", blocked.deadlines)
	}
}

// send refuses once the stream ended, so nothing is queued behind a Bye.
func TestSendAfterTheEndIsRefused(t *testing.T) {
	t.Parallel()
	s := newStream("m1", "boot", "p", 1)
	s.end(agentv1.ByeReason_BYE_REASON_HUB_RESTART, statedb.EndHubRestart)
	if s.send(ack(1)) || len(s.out) != 0 {
		t.Error("send after the end queued a message")
	}
}

// The limiter drops entries older than the window once the map is big.
func TestHelloLimitSweepsOldEntries(t *testing.T) {
	t.Parallel()
	l := newHelloLimit()
	t0 := time.Unix(1_700_000_000, 0)
	for i := range sweepAt {
		l.allow(fmt.Sprintf("m%d", i), t0)
	}
	if len(l.last) != sweepAt {
		t.Fatalf("%d entries, want %d", len(l.last), sweepAt)
	}
	l.allow("fresh", t0.Add(HelloEvery))
	if len(l.last) != 1 {
		t.Errorf("%d entries after the sweep, want only the new one", len(l.last))
	}
}

// A request with a certificate but no ResponseController (the listener's
// own wiring is broken) is the hub's failure, never a missing certificate:
// INTERNAL and connect.failed{step=wiring}.
func TestConnectWithoutAResponseControllerIsInternal(t *testing.T) {
	t.Parallel()
	a, logs := newTestDepsLogged(t)
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "01M"}}
	leaf.URIs = append(leaf.URIs, ca.MachineURI("01M"))
	path, h := agentv1connect.NewAgentServiceHandler(&agentService{enrollDeps: a.enrollDeps, connectDeps: a.connectDeps})
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, leaf)))
	}))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	bs := agentv1connect.NewAgentServiceClient(srv.Client(), srv.URL, connect.WithGRPC()).Connect(t.Context())
	_ = bs.Send(nil)
	_, err := bs.Receive()
	_ = bs.CloseResponse()
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("Connect = %v, want internal", err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if ed, ok := v.(*typev1.ErrorDetail); ok && ed.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
					t.Errorf("reason %v, want INTERNAL", ed.GetReason())
				}
			}
		}
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"connect.failed"`) || !strings.Contains(out, `"step":"wiring"`) ||
		strings.Contains(out, "connect.refused") {
		t.Errorf("logs %s, want connect.failed{step=wiring} and no refusal", out)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
