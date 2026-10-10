package conn_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// pushCommand sends a Command with id on the live stream.
func (h *connectHub) pushCommand(t *testing.T, id string, notAfter time.Time) {
	t.Helper()
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Command{Command: &agentv1.Command{
		CommandId: &typev1.CommandId{Value: id}, Epoch: 1, Seq: 1, NotAfterTime: timestamppb.New(notAfter),
	}}})
}

// pushSettings sends a Settings message of generation gen on the live stream.
func (h *connectHub) pushSettings(t *testing.T, gen uint64) {
	t.Helper()
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Settings{Settings: &agentv1.Settings{Generation: gen}}})
}

// ackOf is a CommandAck for id, the smallest message the queue carries.
func ackOf(id string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandAck{
		CommandAck: &agentv1.CommandAck{CommandId: &typev1.CommandId{Value: id}},
	}}
}

// resultOf is the CommandResult logResult records for id.
func resultOf(id string, st typev1.Status) *agentv1.ConnectRequest {
	return refusalOf(id, st, typev1.ReasonCode_REASON_CODE_UNSPECIFIED)
}

// refusalOf is a CommandResult for id with a status and a reason.
func refusalOf(id string, st typev1.Status, why typev1.ReasonCode) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{
		CommandResult: &agentv1.CommandResult{CommandId: &typev1.CommandId{Value: id}, Status: st, Reason: why},
	}}
}

// progressOf is a CommandProgress for id.
func progressOf(id string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandProgress{
		CommandProgress: &agentv1.CommandProgress{CommandId: &typev1.CommandId{Value: id}},
	}}
}

// acks lists the command ids of the CommandAcks stream n received, in order.
func (h *connectHub) acks(n int) []string {
	var out []string
	for _, m := range h.stream(n).msgs {
		if a := m.GetCommandAck(); a != nil {
			out = append(out, a.GetCommandId().GetValue())
		}
	}
	return out
}

// queued lists what stream n received other than heartbeats: "snapshot",
// "result:<id>", "ack:<id>" and "progress:<id>".
func (h *connectHub) queued(n int) []string {
	var out []string
	for _, m := range h.stream(n).msgs {
		switch {
		case m.GetSnapshot() != nil:
			out = append(out, "snapshot")
		case m.GetCommandResult() != nil:
			out = append(out, "result:"+m.GetCommandResult().GetCommandId().GetValue())
		case m.GetCommandAck() != nil:
			out = append(out, "ack:"+m.GetCommandAck().GetCommandId().GetValue())
		case m.GetCommandProgress() != nil:
			out = append(out, "progress:"+m.GetCommandProgress().GetCommandId().GetValue())
		case m.GetHeartbeat() != nil:
		default:
			out = append(out, fmt.Sprintf("other:%T", m.GetBody()))
		}
	}
	return out
}

// hookLog records what the loop's hooks saw, in order.
type hookLog struct {
	mu   sync.Mutex
	seen []string
}

func (r *hookLog) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, fmt.Sprintf(format, args...))
}

func (r *hookLog) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// logFinished logs id and its OK result in agent.db, as the executor
// would before it sends the result (resultOf(id, STATUS_OK)).
func logFinished(ctx context.Context, db *ring.DB, id string) error {
	now := time.Now()
	if _, _, err := db.LogCommand(ctx, ring.Command{
		ID: id, Kind: "capture_pane", Epoch: 1, Seq: 1, Received: now, NotAfter: now.Add(time.Hour),
	}); err != nil {
		return err
	}
	res, err := proto.Marshal(resultOf(id, typev1.Status_STATUS_OK).GetCommandResult())
	if err != nil {
		return err
	}
	return db.RecordResult(ctx, id, res, now)
}
