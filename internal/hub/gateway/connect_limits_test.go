package gateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// refusedWith checks that s ended with code and reason and that one
// connect.refused line names cause.
func refusedWith(t *testing.T, h *hub, s *agentStream, code connect.Code, reason typev1.ReasonCode, cause string) refusal {
	t.Helper()
	got := refusalOf(t, s.ended(t))
	if got.code != code || got.reason != reason {
		t.Errorf("refusal %+v, want %v %v", got, code, reason)
	}
	if lines := h.events(t, "connect.refused"); len(lines) != 1 || lines[0]["cause"] != cause {
		t.Errorf("connect.refused lines %v, want one with cause %s", lines, cause)
	}
	return got
}

// Connect without a client certificate is refused before anything is
// read: the agent's stream ends though it sends nothing.
func TestConnectNeedsAClientCertificate(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	s := h.open(t, machine{})
	_ = s.bs.Send(nil) // opens the request without a message
	refusedWith(t, h, s, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "no_client_cert")
}

// A hub-CA client certificate without the URI SAN fleet://machine/<CN> is
// not a machine's: refused before anything is read.
func TestConnectRefusesACertificateWithoutTheMachineURI(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := wallNow()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(77), Subject: pkix.Name{CommonName: "01NOSAN"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, h.auth.Cert, pub, h.auth.Key)
	if err != nil {
		t.Fatal(err)
	}
	s := h.open(t, machine{cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}})
	_ = s.bs.Send(nil)
	refusedWith(t, h, s, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "cert_no_san")
}

// A stream that sends nothing ends DEADLINE_EXCEEDED, retryable, after
// the Hello wait, and its handler returns (the stream ends).
func TestConnectEndsAStreamWithNoHelloAfterTheWait(t *testing.T) {
	t.Parallel()
	h := startHub(t, func(o *gateway.ServerOptions) { o.HelloWait = 200 * time.Millisecond })
	m := h.enrollMachine(t)
	s := h.open(t, m)
	_ = s.bs.Send(nil)
	start := time.Now()
	got := refusedWith(t, h, s, connect.CodeDeadlineExceeded, typev1.ReasonCode_REASON_CODE_UNSPECIFIED, "hello_timeout")
	if !got.retryable {
		t.Error("the Hello wait refusal is not retryable")
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Errorf("ended after %v, before the 200 ms wait", took)
	}
}

// A second Hello on a stream that has one ends the stream INVALID_ARGUMENT
// and closes its row as stream_closed.
func TestASecondHelloOnAStreamEndsIt(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, hello(m, 1))
	refusedWith(t, h, s, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "second_hello")
	h.waitConns(t, m.id, "the stream_closed row", func(c []conn) bool { return len(c) == 1 && c[0].endReason == "stream_closed" })
}

// A ConnectReason this hub has no name for is stored as unspecified and
// never refused.
func TestAnUnknownConnectReasonIsStoredAsUnspecified(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	r := hello(m, 0)
	r.GetHello().Reason = agentv1.ConnectReason(99)
	s.send(t, r)
	s.ack(t)
	var reason string
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT connect_reason FROM machine_connections WHERE machine_id = ?`, m.id).
			Scan(&reason)
	})
	if err != nil || reason != "unspecified" {
		t.Errorf("connect_reason %q (%v), want unspecified", reason, err)
	}
}

// One Hello per machine per 5 s (defaults.md), the window marked when the
// limit lets a Hello through: at t+4.99 s it is RATE_LIMITED with
// retry_after 10 ms, retryable; at t+5 s accepted; a Hello refused at
// t+8 s does not move the window, so t+10.5 s is accepted.
func TestTheHelloRateLimit(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	t0 := h.clock.now()
	try := func(at time.Duration, lastEpoch uint64) (*agentStream, error) {
		h.clock.set(t0.Add(at))
		s := h.open(t, m)
		s.send(t, hello(m, lastEpoch))
		select {
		case msg := <-s.msgs:
			if msg.GetHelloAck() == nil {
				t.Fatalf("at +%v: %v, want HelloAck or a refusal", at, msg)
			}
			return s, nil
		case err := <-s.end:
			return nil, err
		case <-time.After(10 * time.Second):
			t.Fatalf("no answer at +%v", at)
		}
		return nil, nil
	}
	if _, err := try(0, 0); err != nil {
		t.Fatalf("first Hello: %v", err)
	}
	_, err := try(4990*time.Millisecond, 1)
	if got := refusalOf(t, err); got.code != connect.CodeResourceExhausted ||
		got.reason != typev1.ReasonCode_REASON_CODE_RATE_LIMITED || !got.retryable {
		t.Fatalf("at +4.99 s: %+v, want RATE_LIMITED, retryable", got)
	}
	if after := retryAfter(t, err); after != 10*time.Millisecond {
		t.Errorf("retry_after %v, want 10ms", after)
	}
	if _, err := try(5*time.Second, 1); err != nil {
		t.Fatalf("at +5 s: %v, want accepted", err)
	}
	if _, err := try(8*time.Second, 2); refusalOf(t, err).code != connect.CodeResourceExhausted {
		t.Fatalf("at +8 s: %v, want RATE_LIMITED", err)
	}
	if _, err := try(10500*time.Millisecond, 2); err != nil {
		t.Fatalf("at +10.5 s: %v, want accepted (the refusal at +8 s moved nothing)", err)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_connect_refused_total", `cause="rate_limited"`); v != 2 {
		t.Errorf("fleet_hub_connect_refused_total{cause=rate_limited} = %v, want 2", v)
	}
}

// A message over MaxMessageBytes ends the stream RESOURCE_EXHAUSTED, and
// the connection row is closed as stream_closed.
func TestAnOversizeMessageEndsTheStream(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	big := &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Snapshot{Snapshot: &agentv1.Snapshot{
		SnapshotId: strings.Repeat("x", gateway.MaxMessageBytes),
	}}}
	_ = s.bs.Send(big) // the hub may reset the stream while it is sent
	if err := s.ended(t); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("stream end %v, want resource_exhausted", err)
	}
	h.waitConns(t, m.id, "the stream_closed row", func(c []conn) bool { return len(c) == 1 && c[0].endReason == "stream_closed" })
}

func retryAfter(t *testing.T, err error) time.Duration {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%v is not a Connect error", err)
	}
	for _, d := range ce.Details() {
		if v, derr := d.Value(); derr == nil {
			if ed, ok := v.(*typev1.ErrorDetail); ok {
				return ed.GetRetryAfter().AsDuration()
			}
		}
	}
	return 0
}
