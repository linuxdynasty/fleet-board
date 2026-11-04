package api_test

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// TestAttachWSNeedsReadScope: a caller without the READ scope is refused
// in the shared serveAttach core on the WebSocket transport too, with a
// SCOPE_MISSING close at 1008.
func TestAttachWSNeedsReadScope(t *testing.T) {
	t.Parallel()
	reg := attach.New(time.Now, nil)
	e := newFleetEnv(t, &typev1.Principal{
		Id:   &typev1.PrincipalId{Value: "u-bob"},
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Name: "bob",
	}, withAttach(reg), withMachineLive(func(string) bool { return true }))

	c := wsDial(t, e, "http://localhost:8080", "fleet.attach.v1")
	defer c.close()
	c.sendMessage(&hubv1.AttachTerminalRequest{Body: &hubv1.AttachTerminalRequest_Open{Open: &hubv1.AttachOpen{
		RequestId: "req-ws-noscope", Session: &typev1.SessionId{Value: "sess-1"},
		Cols: 80, Rows: 24,
	}}})
	closed := c.readMessage()
	if closed.GetClosed().GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR ||
		closed.GetClosed().GetError().GetReason() != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("unscoped WS attach close = %v, want ERROR SCOPE_MISSING", closed)
	}
	if code := c.readCloseCode(); code != 1008 {
		t.Fatalf("unscoped WS attach close code = %d, want 1008", code)
	}
}
