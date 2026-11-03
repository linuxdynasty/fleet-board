package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// attachTransport says which framing the client speaks: the Connect bidi
// stream (a CLI over HTTP/2, headers authenticated, no ticket) or the
// WebSocket (a browser, ticket-bound).
type attachTransport int

const (
	transportConnect attachTransport = iota
	transportWS
)

// clientConn is one AttachTerminal client. *connect.BidiStream satisfies
// it; the WebSocket adapter does too. Send and Receive run on separate
// pump goroutines, never concurrently with themselves.
type clientConn interface {
	Send(*hubv1.AttachTerminalResponse) error
	Receive() (*hubv1.AttachTerminalRequest, error)
}

// firstAttachTimeout is the 10 s a client has for its first frame (02f):
// the hub burns the ticket on that frame, whether or not the attach
// then succeeds.
const firstAttachTimeout = 10 * time.Second

// attachAckWait is the 30 s without an ack while output waits after
// which the hub closes the attach HUB_CLOSED (02f, ADR 0016).
const attachAckWait = 30 * time.Second

// AttachTerminal serves the browser terminal (J1) over Connect bidi
// streaming: a CLI over HTTP/2 with its Bearer [REDACTED] Headers authenticate
// the stream, so no ticket is used. The first message must be an open;
// the hub sends ready, then output, and takes input, resize, select, ack
// and close. A refused or failed attach ends as a Connect error carrying
// the typed detail; a clean one sends its AttachClosed frame and ends nil.
func (f *Fleet) AttachTerminal(ctx context.Context,
	stream *connect.BidiStream[hubv1.AttachTerminalRequest, hubv1.AttachTerminalResponse],
) error {
	if _, err := authorizeScope(ctx, "READ"); err != nil {
		return f.attachRefused(ctx, "", err)
	}
	closed, _ := f.serveAttach(ctx, stream, transportConnect, "")
	if closed == nil {
		// Taken over: a newer open owns the bridge and ends it. End
		// this stream quietly instead of sending a phantom close.
		return nil
	}
	if closed.GetReason() == typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR {
		return connectErrorFor(closed)
	}
	if err := stream.Send(&hubv1.AttachTerminalResponse{
		Body: &hubv1.AttachTerminalResponse_Closed{Closed: closed},
	}); err != nil {
		return err
	}
	return nil
}

// firstReceive reads the first client frame, giving up after the 02f
// allowance. The caller aborts the transport on timeout: the stuck
// Receive ends with the stream and its goroutine with it.
func firstReceive(ctx context.Context, cc clientConn, timeout time.Duration) (*hubv1.AttachTerminalRequest, error) {
	type res struct {
		m   *hubv1.AttachTerminalRequest
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := cc.Receive()
		ch <- res{m, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, context.DeadlineExceeded
	case r := <-ch:
		return r.m, r.err
	}
}

// serveAttach runs one attach from its open frame to its terminal state.
// It returns the AttachClosed the adapters deliver (nil reason means the
// transport died first; the adapters then close quietly) and the 02f
// WebSocket close code. serveAttach never returns a Connect error: the
// Connect adapter maps ERROR closes to one.
func (f *Fleet) serveAttach(ctx context.Context, cc clientConn, transport attachTransport, origin string,
) (*hubv1.AttachClosed, int) {
	// Both transports land here, so the READ scope check lives here too:
	// the HTTP/2 caller already passed it, the WebSocket upgrader did not.
	who, aerr := authorizeScope(ctx, "READ")
	if aerr != nil {
		closed := closedFromError(aerr)
		return closed, wsCodeFor(closed)
	}
	actor := who.GetId().GetValue()
	refuse := func(err error) (*hubv1.AttachClosed, int) {
		closed := closedFromError(err)
		return closed, wsCodeFor(closed)
	}
	first, err := firstReceive(ctx, cc, firstAttachTimeout)
	if err != nil || first.GetOpen() == nil {
		if err == nil {
			err = apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"the first AttachTerminal message must be an open", nil, nil)
		} else if errors.Is(err, context.DeadlineExceeded) {
			err = apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"the first AttachTerminal message arrived after 10 s", nil, nil)
		}
		return refuse(err)
	}
	open := first.GetOpen()
	if open.GetRequestId() == "" {
		return refuse(apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	if open.GetSession() == nil || open.GetSession().GetValue() == "" {
		return refuse(apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "session is required", nil,
			[]*typev1.FieldViolation{{Field: "session", Description: "session is required"}}))
	}
	if !validAttachDims(open.GetCols(), open.GetRows()) {
		return refuse(apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "cols and rows name a terminal", nil,
			[]*typev1.FieldViolation{{Field: "cols", Description: "cols and rows are 1 to 1000"}}))
	}
	readOnly := open.GetReadOnly()
	if !readOnly {
		if _, werr := authorizeScope(ctx, "SESSIONS_WRITE"); werr != nil {
			readOnly = true
		}
	}
	if transport == transportWS {
		// The WebSocket upgrade carries ambient authority, so the first
		// frame must burn a ticket (02f). The HTTP/2 transport ignores
		// the field: its headers authenticate every stream.
		if open.GetTicket() == "" {
			return refuse(apiError(connect.CodeUnauthenticated,
				typev1.ReasonCode_REASON_CODE_ATTACH_TICKET_INVALID,
				"the WebSocket transport needs a CreateAttachTicket ticket", nil, nil))
		}
		if _, err := f.attach.BurnTicket(open.GetTicket(), actor,
			open.GetSession().GetValue(), origin, open.GetReadOnly()); err != nil {
			return refuse(apiError(connect.CodeUnauthenticated,
				typev1.ReasonCode_REASON_CODE_ATTACH_TICKET_INVALID,
				"the attach ticket is unknown, expired or bound to another caller", nil, nil))
		}
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(open)
	if err != nil {
		closed := closedFromError(f.attachFailed(ctx, actor, "encode", err))
		return closed, wsCodeFor(closed)
	}
	b, commandID, adopted, idle, err := f.sendOpenAttach(ctx, who, open, body, readOnly)
	if err != nil {
		return refuse(err)
	}
	gen := b.TakeOver()
	attachID := b.ID()
	sessionID := b.SessionID()
	requestID := open.GetRequestId()
	readyWait, closeWait := f.attachWaits()
	if !adopted || !b.Live() {
		if !waitBridgeLive(ctx, b, readyWait) {
			finish := f.finishAttach(ctx, who, b, gen, sessionID, commandID, requestID, closeWait,
				closedFromError(apiError(connect.CodeInternal,
					typev1.ReasonCode_REASON_CODE_INTERNAL,
					"the agent did not open its Terminal stream in time", nil, nil)), false)
			return finish, wsCodeFor(finish)
		}
	}
	view := b.View()
	cols, rows := b.Size()
	if err := cc.Send(&hubv1.AttachTerminalResponse{
		Body: &hubv1.AttachTerminalResponse_Ready{Ready: &hubv1.AttachReady{
			AttachId: &typev1.AttachId{Value: attachID}, ViewSessionName: view,
			Cols: cols, Rows: rows, ReadOnly: b.ReadOnly(), CommandId: &typev1.CommandId{Value: commandID},
		}},
	}); err != nil {
		finish := f.finishAttach(ctx, who, b, gen, sessionID, commandID, requestID, closeWait,
			&hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED}, false)
		return finish, wsCodeFor(finish)
	}
	closed := f.pumpAttach(ctx, cc, who, b, gen, sessionID, commandID, requestID, idle, closeWait)
	return closed, wsCodeFor(closed)
}

// waitBridgeLive waits for the agent's TermReady up to the allowance.
func waitBridgeLive(ctx context.Context, b *attach.Bridge, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for !b.Live() {
		select {
		case <-ctx.Done():
			return false
		case <-b.Done():
			return b.Live()
		case <-timer.C:
			return b.Live()
		case <-b.Changed():
		}
	}
	return true
}

// finishAttach ends one attach: it asks the agent to stand its view down,
// closes the bridge, stamps attach.close and forgets the open record. Only
// the first finisher runs the end; later pumps only read the terminal
// state. waitConfirm waits for the agent's TermUp closed before closing
// (a client detach, whose reason the agent names); a hub-initiated end
// closes at once with its own reason, so an agent confirm can never
// rename an idle, error or timeout close.
func (f *Fleet) finishAttach(ctx context.Context, who *typev1.Principal, b *attach.Bridge, gen int,
	sessionID, commandID, requestID string, closeWait time.Duration, ours *hubv1.AttachClosed, waitConfirm bool,
) *hubv1.AttachClosed {
	actor := ""
	if who != nil {
		actor = who.GetId().GetValue()
	}
	if b.Finish() {
		b.TakeClose()
		if !waitConfirm && b.Reason() == typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED {
			b.Close(ours.GetReason(), ours.GetError())
		}
		timer := time.NewTimer(closeWait)
		defer timer.Stop()
		if b.Live() && b.Generation() == gen {
			select {
			case <-b.Done():
			case <-timer.C:
			case <-ctx.Done():
			}
		}
		if b.Live() && b.Generation() == gen {
			// The agent missed its TermUp closed: a CloseAttach command
			// kills the PTY and its view destroys itself. Best effort:
			// the close already happened, so failures are only logged.
			f.enqueueCloseAttach(context.WithoutCancel(ctx), who, b.MachineID(), b.ID(), requestID)
		}
		if b.Reason() == typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED {
			b.Close(ours.GetReason(), ours.GetError())
		}
		f.countAttachClosed(ctx, b.Reason())
		f.auditAttachClose(context.WithoutCancel(ctx), who, sessionID, commandID, requestID, b.Reason())
		f.tel.Logger.InfoContext(ctx, "attach.closed", "attach_id", b.ID(),
			"reason", strings.TrimPrefix(b.Reason().String(), "ATTACH_CLOSE_REASON_"), "actor", actor)
	}
	// Whoever closed the bridge first names the end for every pump: this
	// pump's reason only applies when it won the finish. Removing and
	// forgetting are idempotent, so every pump does its own cleanup.
	f.attach.Remove(b)
	f.attach.ForgetOpen(actor, requestID, b.ID())
	if detail := b.Detail(); detail != nil {
		return &hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, Error: detail}
	}
	return &hubv1.AttachClosed{Reason: b.Reason()}
}
