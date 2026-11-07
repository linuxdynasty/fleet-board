package conn

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	agentv1connect "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
)

// liveClient is a real AgentService client against an unreachable hub:
// constructing and opening a stream does no I/O, which is exactly what
// the opener needs to prove.
func liveClient() agentv1connect.AgentServiceClient {
	return agentv1connect.NewAgentServiceClient(http.DefaultClient, "https://example.invalid", connect.WithGRPC())
}

// TestOpenTerminalNeedsSession: with no Connected session the opener
// refuses, so the manager keeps the attach closed instead of dialing
// into the void.
func TestOpenTerminalNeedsSession(t *testing.T) {
	t.Parallel()
	var l Loop
	if _, err := l.OpenTerminal(context.Background()); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("OpenTerminal = %v, want ErrNoTerminal", err)
	}
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.OpenTerminal(dead); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenTerminal on a dead caller = %v, want the caller", err)
	}
}

// TestOpenTerminalFollowsTheSession: a published session opens, a dead
// one refuses, and unpublishing refuses again.
func TestOpenTerminalFollowsTheSession(t *testing.T) {
	t.Parallel()
	var l Loop
	sctx, cancel := context.WithCancel(context.Background())
	l.publishTerminal(liveClient(), sctx)
	st, err := l.OpenTerminal(context.Background())
	if err != nil {
		t.Fatalf("OpenTerminal = %v, want a stream", err)
	}
	if st == nil {
		t.Fatal("OpenTerminal returned a nil stream")
	}
	cancel()
	if _, err := l.OpenTerminal(context.Background()); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("OpenTerminal after the session died = %v, want ErrNoTerminal", err)
	}
	l.unpublishTerminal()
	if _, err := l.OpenTerminal(context.Background()); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("OpenTerminal after unpublish = %v, want ErrNoTerminal", err)
	}
}
