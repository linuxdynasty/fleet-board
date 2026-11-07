package gateway

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// Terminal serves the agent side of terminal attach (J1): the agent dials
// one stream per open attach and the hub pairs it with the client's
// AttachTerminal by attach_id. Pairing and both pumps live in the shared
// ticket and bridge registry; this handler only checks the agent's
// identity from its client certificate, like Connect's.
func (a *agentService) Terminal(ctx context.Context,
	stream *connect.BidiStream[agentv1.TerminalRequest, agentv1.TerminalResponse],
) error {
	machineID, err := terminalPeerID(ctx)
	if err != nil {
		return err
	}
	return a.serveTerminal(ctx, machineID, stream)
}

// serveTerminal pairs one agent stream with its bridge. stream is the
// narrow AgentStream seam so tests drive pairing without a connection.
func (a *agentService) serveTerminal(ctx context.Context, machineID string, stream attach.AgentStream) error {
	return a.attach.ServeAgent(ctx, machineID, stream)
}

// terminalPeerID is the calling agent: the mTLS certificate subject the
// hub pinned at enroll, the same identity Connect checks.
func terminalPeerID(ctx context.Context) (string, error) {
	leaf := peerCert(ctx)
	if leaf == nil || leaf.Subject.CommonName == "" || len(leaf.URIs) != 1 {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("gateway: no agent identity"))
	}
	return leaf.Subject.CommonName, nil
}
