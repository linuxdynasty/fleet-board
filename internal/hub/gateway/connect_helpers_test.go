package gateway_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// agentStream is one Connect stream opened by a test agent; a goroutine
// hands each received message (or the end) over a channel.
type agentStream struct {
	bs     *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse]
	cancel context.CancelFunc
	msgs   chan *agentv1.ConnectResponse
	end    chan error // receives the stream's end once
}

// open starts a Connect stream presenting m's certificate (none for a
// machine{} with no certificate).
func (h *hub) open(t *testing.T, m machine) *agentStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var certs []tls.Certificate
	if m.cert.Certificate != nil {
		certs = append(certs, m.cert)
	}
	s := &agentStream{
		bs: h.client(t, certs...).Connect(ctx), cancel: cancel,
		msgs: make(chan *agentv1.ConnectResponse, 16), end: make(chan error, 1),
	}
	t.Cleanup(func() {
		cancel()
		_ = s.bs.CloseRequest()
		_ = s.bs.CloseResponse()
	})
	go func() {
		for {
			r, err := s.bs.Receive()
			if err != nil {
				s.end <- err
				return
			}
			s.msgs <- r
		}
	}()
	return s
}

// hello is the Hello a real agent sends for m.
func hello(m machine, lastEpoch uint64) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		MachineId: &typev1.MachineId{Value: m.id}, AgentVersion: "test", ProtoMin: 1, ProtoMax: 1,
		BootId: "boot-1", InstallId: "install-1", LastEpoch: lastEpoch,
		Reason: agentv1.ConnectReason_CONNECT_REASON_START,
	}}}
}

func heartbeat() *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{}}}
}

func (s *agentStream) send(t *testing.T, m *agentv1.ConnectRequest) {
	t.Helper()
	if err := s.bs.Send(m); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// next is the next message from the hub, failing the test if the stream
// ends or nothing comes within 10 s.
func (s *agentStream) next(t *testing.T) *agentv1.ConnectResponse {
	t.Helper()
	select {
	case m := <-s.msgs:
		return m
	case err := <-s.end:
		// The receiver queues every message before the end: take one that
		// raced the end.
		s.end <- err
		select {
		case m := <-s.msgs:
			return m
		default:
		}
		t.Fatalf("the stream ended: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no message from the hub in 10 s")
	}
	return nil
}

// ack is the HelloAck the hub must answer first.
func (s *agentStream) ack(t *testing.T) *agentv1.HelloAck {
	t.Helper()
	m := s.next(t)
	if m.GetHelloAck() == nil {
		t.Fatalf("first message %v, want HelloAck", m)
	}
	return m.GetHelloAck()
}

// bye is the hub's Bye, after which the stream must end cleanly.
func (s *agentStream) bye(t *testing.T) agentv1.ByeReason {
	t.Helper()
	m := s.next(t)
	if m.GetBye() == nil {
		t.Fatalf("message %v, want Bye", m)
	}
	if err := s.ended(t); err != nil {
		t.Fatalf("the stream ended with %v after Bye, want a clean end", err)
	}
	return m.GetBye().GetReason()
}

// ended waits for the end of the stream: nil for a clean end.
func (s *agentStream) ended(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.end:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	case m := <-s.msgs:
		t.Fatalf("unexpected message %v, want the end", m)
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end in 10 s")
	}
	return nil
}

// silent fails the test if the hub sends or ends anything within d.
func (s *agentStream) silent(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-s.msgs:
		t.Fatalf("unexpected message %v", m)
	case err := <-s.end:
		t.Fatalf("the stream ended: %v", err)
	case <-time.After(d):
	}
}

// conn is one machine_connections row.
type conn struct {
	epoch     int64
	endReason string
	hbAge     sql.NullInt64
}

func (h *hub) conns(t *testing.T, machineID string) []conn {
	t.Helper()
	var out []conn
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT epoch, ifnull(end_reason, ''), last_heartbeat_age_ms
			FROM machine_connections WHERE machine_id = ? ORDER BY epoch`, machineID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c conn
			if err := rows.Scan(&c.epoch, &c.endReason, &c.hbAge); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// waitConns polls until machineID's rows satisfy ok.
func (h *hub) waitConns(t *testing.T, machineID, what string, ok func([]conn) bool) []conn {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		got := h.conns(t, machineID)
		if ok(got) {
			return got
		}
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s: rows %+v", what, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// metricSum adds every sample of name, whatever its labels.
func metricSum(body, name string) float64 {
	var sum float64
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			f := strings.Fields(line)
			if v, err := strconv.ParseFloat(f[len(f)-1], 64); err == nil {
				sum += v
			}
		}
	}
	return sum
}

// waitMetric polls until the sample of name with labels equals want.
func (h *hub) waitMetric(t *testing.T, name string, want float64, labels ...string) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		got := sample(scrape(t, h.fixture), name, labels...)
		if got == want {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%s%v = %v, want %v", name, labels, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
