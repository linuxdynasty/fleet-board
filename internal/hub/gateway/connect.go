package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// HelloWait is how long the hub waits for a stream's first message
// (defaults.md "Hello wait (hub)"): a stream with no Hello by then ends
// with DEADLINE_EXCEEDED, retryable.
const HelloWait = 30 * time.Second

// Connect is the agent's control stream (02 "Wire model", 02i, 03n W2).
// The machine id and serial come from the verified client certificate,
// never from a message. It checks, in the order of 02g "Hello refusals":
// the certificate (before anything is read), the first message within the
// Hello wait and that it is Hello, machine_id equals the certificate's,
// boot_id and install_id, the proto range, the Hello rate limit, the
// revoked set (revoked-serials, which after a restore may hold serials
// state.db lacks), then statedb.Hello as one writer job. Only after it
// commits is the stream registered and HelloAck sent; a stream it
// displaced gets Bye{DUPLICATE_IDENTITY}. The stream then runs until the
// agent leaves, the transport fails, a newer Hello displaces it or the hub
// shuts down (Bye{HUB_RESTART}).
func (a *agentService) Connect(ctx context.Context, bs *connectStream) error {
	peer := bs.Peer().Addr
	leaf := peerCert(ctx)
	if leaf == nil {
		return a.refuse(ctx, bs, peer, "", helloRefusal{cause: causeNoClientCert})
	}
	machineID, ok := certMachineID(leaf)
	if !ok {
		return a.refuse(ctx, bs, peer, leaf.Subject.CommonName, helloRefusal{cause: causeCertNoSAN})
	}
	rc := responseController(ctx)
	if rc == nil {
		return a.connectFailed(ctx, machineID, "wiring", errNoController)
	}
	first, err := a.firstMessage(bs, rc)
	switch {
	case errors.Is(err, errShuttingDown):
		a.endedBeforeHello(ctx, machineID, "shutdown")
		return sendBye(bs, agentv1.ByeReason_BYE_REASON_HUB_RESTART)
	case errors.Is(err, errHelloWait):
		a.endedBeforeHello(ctx, machineID, "timeout")
		return a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeHelloTimeout})
	case err != nil:
		kind := "reset"
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			kind = "oversize"
		}
		a.endedBeforeHello(ctx, machineID, kind)
		return err
	case first.GetHello() == nil:
		return a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeNotHello})
	}
	ctx, span := a.tracer.Start(ctx, "hub.connect.hello", trace.WithAttributes(attribute.String("machine_id", machineID)))
	s, err := a.hello(ctx, bs, peer, machineID, ca.SerialHex(leaf.SerialNumber), first.GetHello())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "hello")
	}
	if s != nil {
		span.SetAttributes(attribute.Int64("epoch", int64(s.epoch))) //nolint:gosec // G115: epoch <= statedb.MaxEpoch
	}
	span.End()
	if err != nil || s == nil {
		return err
	}
	return a.serve(ctx, bs, rc, s)
}

var (
	// errHelloWait marks a stream that sent nothing within the Hello wait.
	errHelloWait = errors.New("gateway: no Hello within the wait")
	// errShuttingDown marks a stream the shutdown stopped waiting for.
	errShuttingDown = errors.New("gateway: the hub is shutting down")
	// errNoController is the listener's wiring failing: the request has no
	// http.ResponseController (withPeerCert puts one in every request).
	errNoController = errors.New("gateway: no response controller on the request")
)

// firstMessage reads the stream's first message within a.helloWait, or
// until Shutdown begins. Either ends the read through the stream's read
// deadline, which is cleared for the stream's life once the message is
// in; a message that raced either is still answered as that end.
func (a *agentService) firstMessage(bs *connectStream, rc *http.ResponseController) (*agentv1.ConnectRequest, error) {
	wake := func() { _ = rc.SetReadDeadline(time.Now()) }
	t := time.AfterFunc(a.helloWait, wake)
	unwatch := context.AfterFunc(a.stopping, wake)
	m, err := bs.Receive()
	timedOut, stopped := !t.Stop(), !unwatch()
	switch {
	case stopped:
		return nil, errShuttingDown
	case timedOut:
		return nil, errHelloWait
	case err != nil:
		return nil, err
	}
	_ = rc.SetReadDeadline(time.Time{})
	return m, nil
}

// hello runs the Hello checks and transaction. It returns the registered
// stream with HelloAck queued, or nil with the refusal already answered.
func (a *agentService) hello(ctx context.Context, bs *connectStream, peer, machineID, serial string,
	h *agentv1.Hello,
) (*stream, error) {
	if h.GetMachineId().GetValue() != machineID {
		return nil, a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeMachineID})
	}
	for _, v := range []string{h.GetBootId(), h.GetInstallId()} {
		if reason, ok := checkField(v); !ok {
			return nil, a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeField, reason: reason})
		}
	}
	version, ok := protoversion.Negotiate(h.GetProtoMin(), h.GetProtoMax())
	if !ok {
		return nil, a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeProto})
	}
	now := a.now().UTC()
	if ok, wait := a.limit.allow(machineID, now); !ok {
		return nil, a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeRateLimited, retryAfter: wait})
	}
	if a.revoked.Revoked(serial) {
		return nil, a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: causeRevoked})
	}
	// The wait for the job ends when Shutdown begins or the agent leaves;
	// the job itself may still commit, leaving a live row no stream holds
	// (the machine's next Hello closes it as displaced, heartbeat age
	// unknown).
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unwatch := context.AfterFunc(a.stopping, cancel)
	defer unwatch()
	out, err := statedb.Hello(hctx, a.store, statedb.HelloIn{
		MachineID: machineID, PeerSerial: serial, LastEpoch: h.GetLastEpoch(), BootID: h.GetBootId(),
		InstallID: h.GetInstallId(), Peer: peer, Reason: connectReason(h.GetReason()),
		Live: a.streams.live(machineID), Now: now,
	})
	switch {
	case err != nil && ctx.Err() != nil:
		// The agent left while its Hello waited: a departure, not the hub's
		// failure. Its job may still commit, as below.
		a.endedBeforeHello(ctx, machineID, "reset")
		return nil, nil
	case err != nil && a.stopping.Err() != nil:
		a.endedBeforeHello(ctx, machineID, "shutdown")
		return nil, sendBye(bs, agentv1.ByeReason_BYE_REASON_HUB_RESTART)
	}
	if err != nil {
		return nil, a.helloRefused(ctx, bs, peer, machineID, err)
	}
	if a.afterHello != nil {
		a.afterHello()
	}
	s := newStream(machineID, h.GetBootId(), peer, out.Epoch)
	switch got, old := a.streams.register(s); got {
	case registeredClosing:
		// The hub began to shut down while the job ran: close the new row
		// at once and say so.
		s.end(agentv1.ByeReason_BYE_REASON_HUB_RESTART, statedb.EndHubRestart)
		_ = sendBye(bs, agentv1.ByeReason_BYE_REASON_HUB_RESTART)
		a.closed(ctx, s)
		return nil, nil
	case registeredLost:
		// A Hello with a higher epoch committed first and holds the machine:
		// this one's row is already closed as displaced.
		a.duplicate(ctx, s, old)
		_ = sendBye(bs, agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY)
		return nil, nil
	case registeredLive:
		if old != nil {
			a.duplicate(ctx, old, s)
		}
	}
	if a.afterRegister != nil {
		a.afterRegister()
	}
	// A stream that ended between register and here (displaced, or the
	// shutdown) queues no HelloAck and is not an accepted Hello: serve
	// sends its Bye and logs reconnect.closed with the reason.
	if s.send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_HelloAck{HelloAck: a.helloAck(out, version, now)}}) {
		a.accepted(ctx, s, h, out)
	}
	return s, nil
}

func sendBye(bs *connectStream, r agentv1.ByeReason) error {
	return bs.Send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: &agentv1.Bye{Reason: r}}})
}
