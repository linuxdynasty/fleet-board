package daemon_test

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Stopping fleetd run with a live Connect stream says Bye{HUB_RESTART} on
// it, the Connect handler returns, and Run ends well inside the 10 s
// shutdown grace instead of waiting it out (row 5 ruling, amendment 4).
func TestRunStopSaysHubRestartToALiveStream(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	r := h.start(t, h.options())
	res, cert := enrollOverRun(t, h, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bs := agentClient(t, h, r, cert).Connect(ctx)
	defer func() { _ = bs.CloseResponse() }()
	err := bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		MachineId: &typev1.MachineId{Value: res.GetMachineId().GetValue()}, ProtoMin: 1, ProtoMax: 1,
		BootId: "boot-1", InstallId: "install-1", Reason: agentv1.ConnectReason_CONNECT_REASON_START,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := bs.Receive(); err != nil || m.GetHelloAck().GetEpoch() != 1 {
		t.Fatalf("first answer %v, %v; want HelloAck epoch 1", m, err)
	}
	start := time.Now()
	r.stop(t)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Run took %v to stop with a live stream, want well under the 10 s grace", took)
	}
	m, err := bs.Receive()
	if err != nil || m.GetBye().GetReason() != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Errorf("after the stop: %v, %v; want Bye{HUB_RESTART}", m, err)
	}
	if lines := h.logs.events(t, "reconnect.closed"); len(lines) != 1 || lines[0]["reason"] != "hub_restart" {
		t.Errorf("reconnect.closed lines %v, want one hub_restart", lines)
	}
}
