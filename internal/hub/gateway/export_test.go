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

// SetBeforeSnapshotJob makes the server built from o call f in the
// Snapshot arm, on the stream's ingest goroutine, with the stream's
// context, before its writer job.
func SetBeforeSnapshotJob(o *ServerOptions, f func(context.Context)) { o.beforeSnapshot = f }

// SetAfterRegister makes the server built from o call f after a stream is
// registered as its machine's live one, before its HelloAck is queued.
func SetAfterRegister(o *ServerOptions, f func()) { o.afterRegister = f }

// SetAfterHeartbeat makes the server built from o call f each time a
// stream's handler has finished with a Heartbeat wake (its store job, if
// any, committed), with the number of Heartbeats the stream had read.
func SetAfterHeartbeat(o *ServerOptions, f func(n uint64)) { o.afterHeartbeat = f }

// SetBeforeRowClose makes the server built from o call f each time a
// stream's handler is about to close its connection row (not a displaced
// stream's, which closes none).
func SetBeforeRowClose(o *ServerOptions, f func()) { o.beforeRowClose = f }

// SetArmContext gives the server built from o a handler for the arm kind
// that calls f with the stream's context.
func SetArmContext(o *ServerOptions, kind string, f func(context.Context)) {
	if o.arms == nil {
		o.arms = armTable{}
	}
	o.arms.add(kind, func(ctx context.Context, _ *agentService, _ *stream, _ *agentv1.ConnectRequest) { f(ctx) })
}

// Stalled reports whether machineID's live stream has a reader blocked
// handing a message to its full ingest queue.
func Stalled(s *Server, machineID string) bool {
	st := s.connect.streams.get(machineID)
	return st != nil && st.stalled.Load() != 0
}
