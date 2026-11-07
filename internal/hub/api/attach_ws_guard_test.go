package api_test

import (
	"context"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// TestAttachWSOverAckClosesInvalid: acking bytes the hub never sent closes
// the attach INVALID_ARGUMENT instead of zeroing the unacked counter.
func TestAttachWSOverAckClosesInvalid(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e, sess, ticket := wsAttachEnv(t, reg, "req-ws-oack")
	c := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c.close()
	c.sendMessage(&hubv1.AttachTerminalRequest{Body: &hubv1.AttachTerminalRequest_Open{Open: &hubv1.AttachOpen{
		RequestId: "req-ws-oack-open", Session: &typev1.SessionId{Value: sess},
		Cols: 80, Rows: 24, Ticket: ticket,
	}}})
	b := waitBridge(t, e, reg)
	agent := newFakeTerminal()
	agent.recv <- termReadyUp(b.ID(), "fleet-view-"+b.ID(), 80, 24)
	agentErr := make(chan error, 1)
	go func() { agentErr <- reg.ServeAgent(context.Background(), "m1", agent) }()
	if ready := c.readMessage(); ready.GetReady() == nil {
		t.Fatalf("ready = %v, want ready", ready)
	}
	// Nothing was ever pushed, so acking 99 bytes is fabricated.
	c.sendMessage(&hubv1.AttachTerminalRequest{
		Body: &hubv1.AttachTerminalRequest_Ack{Ack: &hubv1.AttachAck{BytesWritten: 99}},
	})
	closed := c.readMessage()
	got := closed.GetClosed().GetError()
	if closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR ||
		got.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("over-ack close = %v, want ERROR INVALID_ARGUMENT", closed)
	}
	if code := c.readCloseCode(); code != 1007 {
		t.Fatalf("over-ack close code = %d, want 1007", code)
	}
	select {
	case err := <-agentErr:
		if err != nil {
			t.Fatalf("ServeAgent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent stream did not end")
	}
}

// TestAttachWSReadOnlyTicketRefusesWritableOpen: a writable open on a
// viewer ticket is refused INVALID_ARGUMENT, never widened to writable.
func TestAttachWSReadOnlyTicketRefusesWritableOpen(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := newFleetEnv(t, localUser("alice"),
		withAttach(reg), withAttachReadyWait(5*time.Second),
		withMachineLive(func(id string) bool { return id == "m1" }))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	res, err := e.client.CreateAttachTicket(context.Background(), ticketReq("req-ws-ro", sess, true))
	if err != nil {
		t.Fatalf("CreateAttachTicket: %v", err)
	}
	c := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c.close()
	c.sendMessage(&hubv1.AttachTerminalRequest{Body: &hubv1.AttachTerminalRequest_Open{Open: &hubv1.AttachOpen{
		RequestId: "req-ws-ro-open", Session: &typev1.SessionId{Value: sess},
		Cols: 80, Rows: 24, Ticket: res.Msg.GetTicket(),
	}}})
	closed := c.readMessage()
	got := closed.GetClosed().GetError()
	if closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR ||
		got.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("writable open on viewer ticket = %v, want ERROR INVALID_ARGUMENT", closed)
	}
	if code := c.readCloseCode(); code != 1007 {
		t.Fatalf("writable open on viewer ticket close code = %d, want 1007", code)
	}
}
