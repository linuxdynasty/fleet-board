package exec

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Store is the command log in agent.db; *ring.DB is one.
type Store interface {
	Command(ctx context.Context, id string) (ring.Entry, error)
	LogCommand(ctx context.Context, c ring.Command) (ring.Entry, bool, error)
	LogAndApply(ctx context.Context, c ring.Command) (ring.Entry, bool, error)
	SetRunning(ctx context.Context, id string, step int) error
	RecordResult(ctx context.Context, id string, result []byte, at time.Time) error
	Open(ctx context.Context) ([]ring.Entry, error)
	Recent(ctx context.Context, since time.Time) ([]ring.Logged, error)
}

// Sender queues an ack, progress or result for the connected stream;
// *conn.Loop is one.
type Sender interface {
	Send(ctx context.Context, m *agentv1.ConnectRequest) error
}

// Policy is what the executor reads of local policy (and, from plan row 6f,
// the effective settings): safe mode, the per-kind rate limit and the
// local approval rules.
type Policy interface {
	SafeMode() bool
	// RateLimit is the per-minute limit for kind; ok is false with no
	// opinion, and DefaultRateLimit applies.
	RateLimit(kind string) (perMinute int, ok bool)
	// ApprovalRequired reports whether a local_approval rule covers kind.
	ApprovalRequired(kind string) bool
}

// Auditor appends a row to the local audit log; *audit.Log is one.
type Auditor interface{ Append(audit.Entry) }

// Approver asks the person at the machine. Await returns once they decide
// (approved, and the channel: "cli" or "notification") or ctx ends. An
// error is no decision: the executor waits out that round (at most 30 s or
// the deadline) and asks again. Await must return soon after ctx ends: the
// executor's Run waits for the command goroutine that calls it. The
// CLI that fills it is plan rows 18, 20 and 24; with none, a wait ends at
// the deadline as LOCAL_CONFIRM_TIMEOUT (ruling fork 4 iv).
type Approver interface {
	Await(ctx context.Context, commandID, kind string) (approved bool, channel string, err error)
}

// Options are the inputs of [New]. DB, Sender, Clock and Policy are needed.
type Options struct {
	DB     Store
	Sender Sender
	// Clock is the strict hub clock (conn.Loop.HubClock): deadlines are
	// checked on it only.
	Clock    conn.HubClock
	Policy   Policy
	Audit    Auditor
	Approver Approver
	// Handlers are the registered kinds. Row 6 registers none.
	Handlers []Handler
	// Home is the user's home folder, written as ~ in result messages.
	Home      string
	Telemetry *telemetry.Telemetry
	// Logger has component and machine_id set; default Telemetry.Logger.
	Logger *slog.Logger
	// Now is the agent clock for received, started and finished times and
	// the rate window; nil is time.Now.
	Now func() time.Time
	// Intake is the room of the intake; zero is DefaultIntake.
	Intake int
}

// DefaultIntake is how many commands wait for the envelope goroutine before
// the hook refuses more (command.intake_full, ruling fork 12 c).
const DefaultIntake = 64

// Executor runs the envelope checks on each command the hub sends, logs it,
// acks it and hands it to its handler (02-contracts "Envelope checks").
type Executor struct {
	opts   Options
	log    *slog.Logger
	reg    registry
	m      *metrics
	intake chan *agentv1.Command
	// resume wakes the envelope goroutine from OnConnected.
	resume chan struct{}

	// Only the envelope goroutine uses these.
	window rateWindow

	refused atomic.Uint64 // results the executor's own checks gave

	mu      sync.Mutex // guards running
	running map[string]*run
	wg      sync.WaitGroup // command goroutines
}

// run is one command handed to its goroutine.
type run struct {
	kind string
	last *agentv1.CommandProgress // re-sent after each new stream's snapshot
}

// New checks opts and builds the executor; call Run to start it.
func New(opts Options) (*Executor, error) {
	if opts.DB == nil || opts.Sender == nil || opts.Clock == nil || opts.Policy == nil {
		return nil, errors.New("exec: DB, Sender, Clock and Policy are needed")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Intake <= 0 {
		opts.Intake = DefaultIntake
	}
	reg, err := newRegistry(opts.Handlers)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil && opts.Telemetry != nil {
		log = opts.Telemetry.Logger.With("component", "exec")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Executor{
		opts: opts, log: log, reg: reg,
		intake:  make(chan *agentv1.Command, opts.Intake),
		resume:  make(chan struct{}, 1),
		running: map[string]*run{},
		window:  rateWindow{times: map[string][]stamp{}},
	}
	if e.m, err = newMetrics(opts.Telemetry, e.runningCount); err != nil {
		return nil, err
	}
	return e, nil
}

// Capabilities is the Hello list (LoopOptions.Capabilities): what the
// registered handlers advertise. Row 6 advertises none.
func (e *Executor) Capabilities() []*typev1.Capability { return e.reg.capabilities() }

// Command is the LoopOptions.Command hook. It runs on the stream's sending
// goroutine, so it only hands c over; false means the intake is full and
// the hub's re-send covers it.
func (e *Executor) Command(_ context.Context, _ uint64, c *agentv1.Command) bool {
	select {
	case e.intake <- c:
		return true
	default:
		return false
	}
}

// OnConnected is the LoopOptions.OnConnected hook, called once a stream's
// snapshot and unacked results are out. Each one runs the resume pass (hub
// time is known only now) and re-sends the last progress of each running
// command (6e round-2 ruling D; ruling fork 4 iii, iv).
func (e *Executor) OnConnected(context.Context, uint64, conn.HubClock) {
	select {
	case e.resume <- struct{}{}:
	default:
	}
}

// Run is the envelope goroutine. It returns when ctx ends, after every
// command goroutine has returned; a command cut off there stays received
// or running in the log and is resumed at the next connect.
func (e *Executor) Run(ctx context.Context) error {
	defer e.m.close()
	defer e.wg.Wait()
	e.seedWindow(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-e.intake:
			e.envelope(ctx, c)
		case <-e.resume:
			e.resumeOpen(ctx)
			e.resendProgress(ctx)
		}
	}
}

// RefusedTotal is how many commands the executor's own checks refused
// since start, logged or not (AgentHealth.commands_refused_total). Safe from
// any goroutine; the heartbeat reads it.
func (e *Executor) RefusedTotal() uint64 { return e.refused.Load() }

func (e *Executor) runningCount() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return int64(len(e.running))
}

// claim adds id to the running set; false when it is there already, so a
// hub re-send and the resume pass never start two runs.
func (e *Executor) claim(id, kind string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[id] != nil {
		return false
	}
	e.running[id] = &run{kind: kind}
	return true
}

func (e *Executor) release(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.running, id)
}

func (e *Executor) setProgress(id string, p *agentv1.CommandProgress) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.running[id]; r != nil {
		r.last = p
	}
}

// resendProgress re-sends the last progress of every running command.
func (e *Executor) resendProgress(ctx context.Context) {
	e.mu.Lock()
	var rs []run
	for _, r := range e.running {
		if r.last != nil {
			rs = append(rs, *r)
		}
	}
	e.mu.Unlock()
	for _, r := range rs {
		e.send(ctx, r.kind, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandProgress{CommandProgress: r.last}})
	}
}

// send queues m. With no stream, or a stream that ends, nothing is lost: a
// result is re-sent from the log after the next snapshot, an ack is
// re-derived by the hub's re-send, a progress re-sent by resendProgress.
// kind is the command's kind, for the log line.
func (e *Executor) send(ctx context.Context, kind string, m *agentv1.ConnectRequest) {
	err := e.opts.Sender.Send(ctx, m)
	if err == nil || errors.Is(err, conn.ErrNoStream) || ctx.Err() != nil {
		return
	}
	args := []any{"err_kind", sendErrKind(err), "kind", kind}
	if id := sentID(m); printableID(id) {
		args = append(args, "command_id", id)
	}
	e.log.WarnContext(ctx, "command.send_failed", args...)
}

// sentID is the command_id an ack, progress or result is about.
func sentID(m *agentv1.ConnectRequest) string {
	switch {
	case m.GetCommandAck() != nil:
		return m.GetCommandAck().GetCommandId().GetValue()
	case m.GetCommandProgress() != nil:
		return m.GetCommandProgress().GetCommandId().GetValue()
	default:
		return m.GetCommandResult().GetCommandId().GetValue()
	}
}

func sendErrKind(err error) string {
	switch {
	case errors.Is(err, conn.ErrResultNotLogged):
		return "not_logged"
	case errors.Is(err, conn.ErrQueueFull):
		return "queue_full"
	default:
		return "other"
	}
}
