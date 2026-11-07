package attach

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// AgentStream is one agent Terminal stream: the agent dials it and the
// hub pairs it with a bridge by attach_id. *connect.BidiStream satisfies
// it; tests use a fake.
type AgentStream interface {
	Receive() (*agentv1.TerminalRequest, error)
	Send(*agentv1.TerminalResponse) error
}

// ServeAgent pairs one agent Terminal stream with its bridge and pumps
// both directions until either end closes. The first TermUp must be
// ready for a pending bridge of this machine; anything else gets a
// TermDown close so a stale agent tears its view session down instead of
// hanging. A dead stream closes its bridge AGENT_OFFLINE, so the browser
// hears try-again instead of hanging too. It returns transport errors;
// pairing refusals are protocol, not failure, and return nil.
func (r *Registry) ServeAgent(ctx context.Context, machineID string, s AgentStream) error {
	req, err := s.Receive()
	if err != nil {
		return err
	}
	up := req.GetUp()
	attachID := up.GetAttachId().GetValue()
	ready, ok := up.GetBody().(*agentv1.TermUp_Ready)
	b, found := r.Get(attachID)
	switch {
	case attachID == "" || !found || !ok:
		// Unknown attach, or the first message is not ready: tell the
		// agent to stand its view down when we know which attach it
		// means, so a stale dial never hangs.
		if attachID != "" {
			_ = s.Send(&agentv1.TerminalResponse{Down: &agentv1.TermDown{
				AttachId: &typev1.AttachId{Value: attachID},
				Body:     &agentv1.TermDown_Close{Close: &agentv1.TermClose{}},
			}})
		}
		return nil
	case b.MachineID() != machineID:
		_ = s.Send(&agentv1.TerminalResponse{Down: &agentv1.TermDown{
			AttachId: &typev1.AttachId{Value: attachID},
			Body:     &agentv1.TermDown_Close{Close: &agentv1.TermClose{}},
		}})
		return nil
	case !b.Claim():
		_ = s.Send(&agentv1.TerminalResponse{Down: &agentv1.TermDown{
			AttachId: &typev1.AttachId{Value: attachID},
			Body:     &agentv1.TermDown_Close{Close: &agentv1.TermClose{}},
		}})
		return nil
	}
	defer b.Release()
	defer r.Remove(b)
	got := ready.Ready
	b.Ready(got.GetViewSessionName(), got.GetCols(), got.GetRows())
	readerDone := make(chan error, 1)
	go func() {
		for {
			if b.Suspended() {
				// Past the high-water mark the hub stops reading: HTTP/2
				// flow control blocks the agent's PTY read, and Connect
				// commands on their own stream never wait. Acks wake us.
				select {
				case <-ctx.Done():
					readerDone <- ctx.Err()
					return
				case <-b.Done():
					readerDone <- nil
					return
				case <-b.Changed():
				}
				continue
			}
			req, err := s.Receive()
			if err != nil {
				b.Close(typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_OFFLINE, nil)
				readerDone <- err
				return
			}
			up := req.GetUp()
			if up.GetAttachId().GetValue() != attachID {
				continue
			}
			switch body := up.GetBody().(type) {
			case *agentv1.TermUp_Output:
				if err := b.PushOutput(body.Output); err != nil {
					readerDone <- nil
					return
				}
			case *agentv1.TermUp_Closed:
				b.Close(body.Closed.GetReason(), nil)
				readerDone <- nil
				return
			default:
				// A second ready or an unknown body: ignore, the
				// bridge already stands.
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			// Hub shutdown (or revoke): the connection is going away.
			// The reader ends with the stream; never wait for it here.
			b.Close(typev1.AttachCloseReason_ATTACH_CLOSE_REASON_HUB_CLOSED, nil)
			return ctx.Err()
		case <-b.Done():
			// Last words (usually the TermDown close the client end
			// queued), then return: the reader ends when the agent
			// answers or its stream dies, and never blocks this return.
			for _, down := range b.DrainDown() {
				_ = s.Send(&agentv1.TerminalResponse{Down: down})
			}
			select {
			case err := <-readerDone:
				return err
			default:
				return nil
			}
		case <-b.Changed():
			for _, down := range b.DrainDown() {
				if err := s.Send(&agentv1.TerminalResponse{Down: down}); err != nil {
					b.Close(typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_OFFLINE, nil)
					return err
				}
			}
		}
	}
}
