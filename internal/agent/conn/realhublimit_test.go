package conn_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// sameMachineHello opens a Connect stream with a's own certificate (a
// copied state directory) and returns its HelloAck epoch.
func sameMachineHello(t *testing.T, h *conntest.Hub, a *agent) uint64 {
	t.Helper()
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair([]byte(id.CertPEM), []byte(id.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(id.CAPEM)) {
		t.Fatal("no CA in identity.json")
	}
	hc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert},
		},
		ForceAttemptHTTP2: true,
	}}
	t.Cleanup(hc.CloseIdleConnections)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bs := agentv1connect.NewAgentServiceClient(hc, "https://"+h.Addr, connect.WithGRPC()).Connect(ctx)
	err = bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		MachineId: &typev1.MachineId{Value: id.MachineID}, ProtoMin: 1, ProtoMax: 1, BootId: "boot-copy",
		InstallId: "install-copy", LastEpoch: agentEpoch(t, a), Reason: agentv1.ConnectReason_CONNECT_REASON_START,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := bs.Receive()
	if err != nil || m.GetHelloAck() == nil {
		t.Fatalf("copied-machine Hello: %v, %v; want HelloAck", m, err)
	}
	return m.GetHelloAck().GetEpoch()
}

// The hub's Hello rate limit against the real connection loop: displaced
// by a copy of its own state directory, the agent reconnects at once and
// is refused RATE_LIMITED again and again (connect.failed, backoff, the
// loop never ends), then connects once the hub's 5 s have passed.
func TestTheLoopBacksOffThroughTheHubsHelloRateLimit(t *testing.T) {
	t.Parallel()
	hubClock := &skewClock{}
	h := conntest.StartHub(t, conntest.Options{Now: hubClock.now})
	a, r, id := realHubAgent(t, h, nil)
	r.connected(t)
	hubClock.set(6 * time.Second)
	if ep := sameMachineHello(t, h, a); ep != 2 {
		t.Fatalf("the copy got epoch %d, want 2", ep)
	}
	const refusals = 3
	waitFor(t, 10*time.Second, "three rate-limit refusals", func() bool {
		return len(h.Events(t, "connect.refused")) >= refusals
	})
	r.running(t)
	hubClock.set(12 * time.Second)
	waitFor(t, 10*time.Second, "the agent's epoch 3", func() bool { return h.LastEpoch(t, id) == 3 })
	r.connected(t)
	for _, l := range h.Events(t, "connect.refused") {
		if l["cause"] != "rate_limited" {
			t.Errorf("refusal %v, want only rate_limited", l)
		}
	}
	failed := 0
	for _, l := range a.events(t, "connect.failed") {
		if l["step"] == "hello" {
			failed++
		}
	}
	if failed < refusals {
		t.Errorf("%d connect.failed{step=hello} lines on the agent, want at least %d", failed, refusals)
	}
}
