package conn

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	agentv1connect "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
)

// ErrNoTerminal means no session to the hub is Connected, or the one that
// is has not finished its hello: there is no stream to open a Terminal on.
// The caller keeps the attach closed: the hub retries OpenAttach once the
// agent is back (TMUX_SERVER_DOWN is retryable).
var ErrNoTerminal = errors.New("conn: no connected session for a terminal stream")

// terminalLive is the Connected session's Terminal opener: the link's
// client and the session context, so a Terminal stream dies with its
// session and never outlives it.
type terminalLive struct {
	client agentv1connect.AgentServiceClient
	sctx   context.Context
}

// publishTerminal records the live session; only the serving goroutine
// stores it. unpublishTerminal clears it when the session ends, so a
// Terminal opened afterwards dials on the next session, never a dead one.
func (l *Loop) publishTerminal(client agentv1connect.AgentServiceClient, sctx context.Context) {
	l.term.Store(&terminalLive{client: client, sctx: sctx})
}

func (l *Loop) unpublishTerminal() { l.term.Store(nil) }

// OpenTerminal opens an Agent Terminal bidi stream on the Connected
// session, for the attach manager's Dial. The stream hangs off the
// session's context, not ctx: ctx only bounds this call, while the stream
// must outlive the command that opened it and die with the session.
func (l *Loop) OpenTerminal(ctx context.Context) (*connect.BidiStreamForClient[agentv1.TerminalRequest, agentv1.TerminalResponse], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v := l.term.Load()
	if v == nil || v.sctx.Err() != nil {
		return nil, ErrNoTerminal
	}
	return v.client.Terminal(v.sctx), nil
}
