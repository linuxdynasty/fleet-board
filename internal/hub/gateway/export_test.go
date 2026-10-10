package gateway

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// SetAfterHelloCommit makes the server built from o call f after each
// Hello transaction commits, before the stream is registered.
func SetAfterHelloCommit(o *ServerOptions, f func()) { o.afterHello = f }

// BeginShutdown runs what http.Server.Shutdown runs first on this server:
// the shutdown hook that ends every Connect stream.
func BeginShutdown(s *Server) { s.connect.beginShutdown() }

// SetArm gives the server built from o a handler for the arm kind that
// calls f.
func SetArm(o *ServerOptions, kind string, f func()) {
	if o.arms == nil {
		o.arms = armTable{}
	}
	o.arms.add(kind, func(context.Context, *agentService, *stream, *agentv1.ConnectRequest) { f() })
}

// SetAfterRegister makes the server built from o call f after a stream is
// registered as its machine's live one, before its HelloAck is queued.
func SetAfterRegister(o *ServerOptions, f func()) { o.afterRegister = f }
