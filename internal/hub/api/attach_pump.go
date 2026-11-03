package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// pumpAttach moves bytes and control between the client and the bridge
// until one end closes, the client starves acks or idles out, or a newer
// open takes the bridge over. Every exit but a takeover finishes the
// attach exactly once through finishAttach; a takeover returns nil and
// sends nothing, leaving the end to the new owner.
func (f *Fleet) pumpAttach(ctx context.Context, cc clientConn, who *typev1.Principal, b *attach.Bridge, gen int,
	sessionID, commandID, requestID string, idle, closeWait time.Duration,
) *hubv1.AttachClosed {
	type recvRes struct {
		m   *hubv1.AttachTerminalRequest
		err error
	}
	recvCh := make(chan recvRes, 16)
	sendErr := make(chan error, 1)
	// sentCh fires after each output handed to the transport, so flowing
	// output counts as activity even when the client is slow to ack it.
	sentCh := make(chan struct{}, 1)
	// The pumps end with the transport: the adapter closes it when
	// serveAttach returns, which fails the stuck Send or Receive.
	go func() {
		for {
			if b.Generation() != gen {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-b.Done():
				return
			default:
			}
			out := b.NextOutput(attach.OutputChunk)
			if len(out) == 0 {
				select {
				case <-ctx.Done():
					return
				case <-b.Done():
					return
				case <-b.Changed():
				}
				continue
			}
			if err := cc.Send(&hubv1.AttachTerminalResponse{
				Body: &hubv1.AttachTerminalResponse_Output{Output: out},
			}); err != nil {
				sendErr <- err
				return
			}
			select {
			case sentCh <- struct{}{}:
			default:
			}
		}
	}()
	go func() {
		for {
			m, err := cc.Receive()
			if err != nil {
				select {
				case recvCh <- recvRes{err: err}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case recvCh <- recvRes{m: m}:
			case <-ctx.Done():
				return
			}
		}
	}()
	finish := func(ours *hubv1.AttachClosed, waitConfirm bool) *hubv1.AttachClosed {
		return f.finishAttach(ctx, who, b, gen, sessionID, commandID, requestID, closeWait, ours, waitConfirm)
	}
	detached := func(waitConfirm bool) *hubv1.AttachClosed {
		return finish(&hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED}, waitConfirm)
	}
	var ackedTotal uint64
	lastAck := time.Now()
	lastActivity := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Hub shutdown (or revoke): the connection is going away.
			return finish(&hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_HUB_CLOSED}, false)
		case <-b.Done():
			// The agent end closed first: its reason names the end.
			return finish(&hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED}, true)
		case err := <-sendErr:
			if err != nil {
				return finish(&hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED}, false)
			}
		case r := <-recvCh:
			if r.err != nil {
				return finish(&hubv1.AttachClosed{Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED}, false)
			}
			switch body := r.m.GetBody().(type) {
			case *hubv1.AttachTerminalRequest_Input:
				if len(body.Input) > attach.InputMax {
					return finish(closedFromError(apiError(connect.CodeInvalidArgument,
						typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
						"input is at most 4 KiB per message", nil, nil)), false)
				}
				if !b.TakeInput(body.Input) && b.Reason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED {
					return detached(false)
				}
				lastActivity = time.Now()
			case *hubv1.AttachTerminalRequest_Resize:
				if !validAttachDims(body.Resize.GetCols(), body.Resize.GetRows()) {
					return finish(closedFromError(apiError(connect.CodeInvalidArgument,
						typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
						"cols and rows name a terminal", nil, nil)), false)
				}
				if !b.TakeResize(body.Resize.GetCols(), body.Resize.GetRows()) {
					return detached(false)
				}
				lastActivity = time.Now()
			case *hubv1.AttachTerminalRequest_Select:
				if !validAttachSelect(body.Select) {
					return finish(closedFromError(apiError(connect.CodeInvalidArgument,
						typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
						"select names a window or pane of the attached session", nil, nil)), false)
				}
				sel := body.Select
				if w := sel.GetWindow(); w != nil {
					if !b.TakeSelectWindow(w.GetServer(), w.GetServerStartId(), w.GetWindowId()) {
						return detached(false)
					}
				} else if p := sel.GetPane(); p != nil {
					if !b.TakeSelectPane(p.GetServer(), p.GetServerStartId(), p.GetPaneId()) {
						return detached(false)
					}
				}
				lastActivity = time.Now()
			case *hubv1.AttachTerminalRequest_Ack:
				if w := body.Ack.GetBytesWritten(); w > ackedTotal {
					if err := b.Ack(w - ackedTotal); err != nil {
						return finish(closedFromError(apiError(connect.CodeInvalidArgument,
							typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
							"ack counts bytes the hub never sent", nil, nil)), false)
					}
					ackedTotal = w
				}
				lastAck = time.Now()
				// Acks arrive while output flows, so they prove the
				// client is alive as much as keystrokes do (02f).
				lastActivity = time.Now()
			case *hubv1.AttachTerminalRequest_Close:
				return detached(true)
			case *hubv1.AttachTerminalRequest_Open:
				return finish(closedFromError(apiError(connect.CodeInvalidArgument,
					typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
					"one open per AttachTerminal stream", nil, nil)), false)
			case nil:
				// An empty message carries nothing; ignore it so a
				// newer client field never breaks an older hub.
			}
		case <-sentCh:
			// Output reached the transport: the session is producing,
			// not sitting idle, even before the client acks it.
			lastActivity = time.Now()
		case now := <-ticker.C:
			if b.Generation() != gen {
				return nil
			}
			// Sent-but-unacked output arms the wait too: NextOutput pops
			// the queue on send, so queued-only bytes would never fire
			// once the client holds everything without acking.
			if (b.OutputBytes() > 0 || b.Unacked() > 0) && now.Sub(lastAck) > attachAckWait {
				return finish(&hubv1.AttachClosed{
					Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_HUB_CLOSED,
				}, false)
			}
			if idle > 0 && now.Sub(lastActivity) > idle {
				return finish(&hubv1.AttachClosed{
					Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_IDLE_TIMEOUT,
				}, false)
			}
		}
	}
}

// validAttachSelect reports whether a select names a window or a pane of
// the attached session. The agent runs it in the view session only, so a
// key outside the session must never reach tmux: the agent re-checks, and
// the hub refuses the frame first.
func validAttachSelect(sel *hubv1.AttachSelect) bool {
	if sel == nil {
		return false
	}
	if w := sel.GetWindow(); w != nil {
		return w.GetServer() != "" && w.GetServerStartId() != "" && w.GetWindowId() != ""
	}
	if p := sel.GetPane(); p != nil {
		return p.GetServer() != "" && p.GetServerStartId() != "" && p.GetPaneId() != ""
	}
	return false
}

// closedFromError renders a caller error as the terminal ERROR close: the
// adapters deliver the typed detail (a frame on the WebSocket, a trailer
// on HTTP/2). A non-Connect failure is the hub's own: INTERNAL.
func closedFromError(err error) *hubv1.AttachClosed {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return &hubv1.AttachClosed{
			Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR,
			Error:  &typev1.ErrorDetail{Reason: typev1.ReasonCode_REASON_CODE_INTERNAL},
		}
	}
	for _, d := range ce.Details() {
		if msg, derr := d.Value(); derr == nil {
			if ed, ok := msg.(*typev1.ErrorDetail); ok {
				return &hubv1.AttachClosed{
					Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, Error: ed,
				}
			}
		}
	}
	return &hubv1.AttachClosed{
		Reason: typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR,
		Error:  &typev1.ErrorDetail{Reason: reasonForCode(ce.Code())},
	}
}

// reasonForCode maps a bare Connect code to its closest attach reason.
func reasonForCode(code connect.Code) typev1.ReasonCode {
	switch code {
	case connect.CodeUnauthenticated:
		return typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID
	case connect.CodePermissionDenied:
		return typev1.ReasonCode_REASON_CODE_SCOPE_MISSING
	case connect.CodeNotFound:
		return typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND
	case connect.CodeAlreadyExists:
		return typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT
	case connect.CodeInvalidArgument:
		return typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT
	case connect.CodeResourceExhausted:
		return typev1.ReasonCode_REASON_CODE_RATE_LIMITED
	case connect.CodeUnavailable:
		return typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE
	default:
		return typev1.ReasonCode_REASON_CODE_INTERNAL
	}
}
