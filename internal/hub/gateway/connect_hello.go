package gateway

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// helloRefused maps a statedb.Hello error to its answer.
func (a *agentService) helloRefused(ctx context.Context, bs *connectStream, peer, machineID string, err error) error {
	cause := ""
	switch {
	case errors.Is(err, statedb.ErrMachineUnknown):
		cause = causeUnknown
	case errors.Is(err, statedb.ErrMachineRevoked), errors.Is(err, statedb.ErrSerialRevoked):
		cause = causeRevoked
	case errors.Is(err, statedb.ErrPeerSerialUnknown):
		cause = causeSerialOther
	case errors.Is(err, statedb.ErrEpochOutOfRange):
		cause = causeEpochRange
	case errors.Is(err, statedb.ErrHelloInvalid):
		// The gateway checked the agent's fields; what is left is the hub's.
		return a.connectFailed(ctx, machineID, "validate", err)
	default:
		return a.connectFailed(ctx, machineID, "store", err)
	}
	return a.refuse(ctx, bs, peer, machineID, helloRefusal{cause: cause})
}

// checkField applies the 02g install_id rule to boot_id and install_id:
// 1 to 64 printable ASCII characters without spaces. Too long is
// FIELD_TOO_LARGE, any other break INVALID_ARGUMENT.
func checkField(v string) (typev1.ReasonCode, bool) {
	if checkInstallID(v) == nil {
		return typev1.ReasonCode_REASON_CODE_UNSPECIFIED, true
	}
	if len(v) > maxInstallID {
		return typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, false
	}
	return typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, false
}

// helloAck is what the agent checks (session.go accept: fleet_id, the
// proto version in its range, an epoch above its last and in range, the
// seq floor, hub_time, the heartbeat interval, the message cap, the hub
// addresses) plus settings_epoch and this process's hub_instance_id. No
// hub_capabilities: snapshots arrive whole, at most MaxMessageBytes (row 5
// ruling, amendment 7).
func (a *agentService) helloAck(out statedb.HelloOut, version uint32, now time.Time) *agentv1.HelloAck {
	return &agentv1.HelloAck{
		HubVersion: a.version, HubTime: timestamppb.New(now), Epoch: out.Epoch, ProtoVersion: version,
		HubLastEventSeq: out.HubLastEventSeq, MaxMessageBytes: MaxMessageBytes,
		HeartbeatInterval: durationpb.New(HeartbeatInterval), SettingsGeneration: out.SettingsGeneration,
		HubInstanceId: a.instanceID, SettingsEpoch: out.SettingsEpoch, HubAddresses: a.id.HubAddresses,
		FleetId: a.id.FleetID,
	}
}

// HeartbeatInterval is HelloAck.heartbeat_interval (defaults.md: 15 s).
const HeartbeatInterval = 15 * time.Second

// certMachineID is the machine id of a hub-issued client certificate: its
// CN, which its one URI SAN (fleet://machine/<id>) must name too (02g).
func certMachineID(leaf *x509.Certificate) (string, bool) {
	if leaf.Subject.CommonName == "" || len(leaf.URIs) != 1 {
		return "", false
	}
	id := leaf.Subject.CommonName
	return id, leaf.URIs[0].String() == ca.MachineURI(id).String()
}

// connectReason is Hello.reason as stored; a value this hub has no name
// for is stored as unspecified, never refused (refusal ruling, item 5).
func connectReason(r agentv1.ConnectReason) statedb.ConnectReason {
	switch r {
	case agentv1.ConnectReason_CONNECT_REASON_START:
		return statedb.ConnectStart
	case agentv1.ConnectReason_CONNECT_REASON_RECONNECT:
		return statedb.ConnectReconnect
	case agentv1.ConnectReason_CONNECT_REASON_WAKE:
		return statedb.ConnectWake
	case agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE:
		return statedb.ConnectNetworkChange
	default:
		return statedb.ConnectUnspecified
	}
}

type controllerKey struct{}

// responseController is the stream's http.ResponseController (read and
// write deadlines end a stream whose agent stopped reading or writing).
func responseController(ctx context.Context) *http.ResponseController {
	rc, _ := ctx.Value(controllerKey{}).(*http.ResponseController)
	return rc
}
