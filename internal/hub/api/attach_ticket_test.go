package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func ticketReq(id, sess string, readOnly bool) *connect.Request[hubv1.CreateAttachTicketRequest] {
	req := connect.NewRequest(&hubv1.CreateAttachTicketRequest{
		RequestId: id, Session: &typev1.SessionId{Value: sess}, ReadOnly: readOnly,
	})
	req.Header().Set("Origin", "http://localhost:8080")
	return req
}

// TestCreateAttachTicketMintsOnce: a ticket mints with a 60 s expiry, and
// the secret is shown once: the same request replays ALREADY_EXISTS with
// SECRET_ALREADY_DELIVERED, never the ticket again.
func TestCreateAttachTicketMintsOnce(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	res, err := e.client.CreateAttachTicket(context.Background(), ticketReq("req-tick-1", sess, false))
	if err != nil {
		t.Fatalf("CreateAttachTicket: %v", err)
	}
	if res.Msg.GetTicket() == "" {
		t.Fatal("empty ticket")
	}
	if res.Msg.GetReadOnly() {
		t.Fatal("owner ticket clamped to read-only, want read-write")
	}
	ttl := res.Msg.GetExpireTime().AsTime().Sub(fleetNow)
	if ttl < 59*time.Second || ttl > time.Minute {
		t.Fatalf("ticket TTL = %v, want about 60s", ttl)
	}

	// Same request_id, same body: the secret is never re-shown.
	_, err = e.client.CreateAttachTicket(context.Background(), ticketReq("req-tick-1", sess, false))
	code, reason, _ := detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED {
		t.Fatalf("replay = %v %v, want AlreadyExists SECRET_ALREADY_DELIVERED", code, reason)
	}

	// Same request_id, another body: conflict.
	other := ticketReq("req-tick-1", sess, true)
	_, err = e.client.CreateAttachTicket(context.Background(), other)
	code, reason, _ = detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Fatalf("conflict = %v %v, want AlreadyExists REQUEST_ID_CONFLICT", code, reason)
	}
}

// TestCreateAttachTicketRefusals: request_id is required and the session
// must be one the hub started.
func TestCreateAttachTicketRefusals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")

	bad := ticketReq("", sess, false)
	if _, err := e.client.CreateAttachTicket(context.Background(), bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty request_id code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	// A developer-created tmux session has no hub_sessions row (only
	// hub-initiated session create writes one), so it lands here too:
	// J1 covers hub-started sessions until the session mirror lands.
	unknown := ticketReq("req-tick-unknown", "nope", false)
	_, err := e.client.CreateAttachTicket(context.Background(), unknown)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session code = %v, want NotFound", connect.CodeOf(err))
	}
}
