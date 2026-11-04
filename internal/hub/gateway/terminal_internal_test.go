package gateway

import (
	"bytes"
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// TestTerminalPeerID: the agent is its certificate subject; anything else
// is unauthenticated.
func TestTerminalPeerID(t *testing.T) {
	t.Parallel()
	if _, err := terminalPeerID(context.Background()); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no cert = %v, want Unauthenticated", err)
	}
	withCert := func(cert *x509.Certificate) context.Context {
		return context.WithValue(context.Background(), peerKey{}, cert)
	}
	good := &x509.Certificate{
		Subject: pkix.Name{CommonName: "m1"},
		URIs:    []*url.URL{{Scheme: "spiffe", Host: "fleet", Path: "/m1"}},
	}
	if id, err := terminalPeerID(withCert(good)); err != nil || id != "m1" {
		t.Fatalf("good cert = %q, %v, want m1, nil", id, err)
	}
	noURI := &x509.Certificate{Subject: pkix.Name{CommonName: "m1"}}
	if _, err := terminalPeerID(withCert(noURI)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no URI = %v, want Unauthenticated", err)
	}
	noCN := &x509.Certificate{URIs: []*url.URL{{Scheme: "spiffe", Host: "fleet"}}}
	if _, err := terminalPeerID(withCert(noCN)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no CN = %v, want Unauthenticated", err)
	}
}

// fakeTermStream is a scripted agent Terminal stream.
type fakeTermStream struct {
	mu   sync.Mutex
	recv chan *agentv1.TerminalRequest
	sent []*agentv1.TermDown
}

func (f *fakeTermStream) Receive() (*agentv1.TerminalRequest, error) {
	req, ok := <-f.recv
	if !ok {
		return nil, errors.New("fake: stream closed")
	}
	return req, nil
}

func (f *fakeTermStream) Send(res *agentv1.TerminalResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if res.GetDown() != nil {
		f.sent = append(f.sent, res.GetDown())
	}
	return nil
}

func (f *fakeTermStream) downs() []*agentv1.TermDown {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentv1.TermDown(nil), f.sent...)
}

// TestServeTerminalPairs: the first TermUp ready pairs the pending
// bridge; output queues for the client, a TermDown close reaches the
// agent, and the agent's closed ends the bridge with its reason.
func TestServeTerminalPairs(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	a, _ := newTestDepsTo(t, &logs)
	a.attach = attach.New(time.Now, nil)
	b, _, err := a.attach.OpenOrAdopt("p", "req-1", [32]byte{1}, "attach-1", "m1", "sess-1", false)
	if err != nil {
		t.Fatalf("OpenOrAdopt: %v", err)
	}
	b.SetMachine("m1")
	fake := &fakeTermStream{recv: make(chan *agentv1.TerminalRequest, 16)}
	fake.recv <- &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: "attach-1"},
		Body:     &agentv1.TermUp_Ready{Ready: &agentv1.TermReady{ViewSessionName: "fleet-view-attach-1"}},
	}}
	done := make(chan error, 1)
	go func() { done <- a.serveTerminal(context.Background(), "m1", fake) }()
	deadline := time.Now().Add(5 * time.Second)
	for !b.Live() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !b.Live() {
		t.Fatal("bridge never went live")
	}
	if got := b.View(); got != "fleet-view-attach-1" {
		t.Fatalf("view = %q", got)
	}
	if !b.TakeClose() {
		t.Fatal("TakeClose refused on a live bridge")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		var found bool
		for _, d := range fake.downs() {
			if d.GetClose() != nil {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never got TermDown close")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fake.recv <- &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: "attach-1"},
		Body:     &agentv1.TermUp_Closed{Closed: &agentv1.TermClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_SESSION_ENDED}},
	}}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveTerminal: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveTerminal did not end")
	}
	if got := b.Reason(); got != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_SESSION_ENDED {
		t.Fatalf("reason = %v, want SESSION_ENDED", got)
	}
}
