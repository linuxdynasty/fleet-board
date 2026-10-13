package inventory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Emitter takes one event (events.Emitter): it stamps the seq and, for a
// NEVER_DROP or COALESCE event, waits for the ring commit.
type Emitter interface {
	Emit(ctx context.Context, ev *agentv1.Event) (uint64, error)
}

// Options configure an [Observer].
type Options struct {
	// Program is the tmux binary the machine facts found.
	Program string
	// Runner runs the clients' one-shot tmux commands (caller "tmux").
	Runner *proc.Runner
	// Telemetry is where signals go; nil counts and logs nowhere.
	Telemetry *telemetry.Telemetry
	// Emitter takes the lifecycle events; nil sends none (the Snapshot
	// still carries the inventory).
	Emitter Emitter
	// Folder is the socket folder; "" is SocketFolder of the agent's own
	// environment and uid.
	Folder string
	// Servers are the socket names watched, each a PaneKey.server; nil is
	// "default" and "fleet-probe" (row-7 ruling fork 4(a)).
	Servers []string
	// NewWatcher replaces the fsnotify watch (tests).
	NewWatcher func() (Watcher, error)
	// Notify, when set, also gets every control-mode message of each
	// server (7e and 7f read them): on the client's reader goroutine, so it
	// must not block or call the client.
	Notify func(server string, m tmux.Message)
	// IdleThreshold is the output-quiet edge of E4 activity; 0 is the
	// 2-minute default (defaults.md `idle_threshold`). Only a test sets a
	// shorter one, to watch panes go idle fast.
	IdleThreshold time.Duration

	// maxReply is each client's reply bound (0: the parser's default);
	// only a test sets it, to make a read fail (export_test.go).
	maxReply int

	// inputPoll is the backup list-clients poll gap (0: inputPollGap);
	// only a test sets it, to watch the poll run and stop fast
	// (export_test.go).
	inputPoll time.Duration

	// emitGap is the least time between two PaneInput events of one pane
	// (0: defaultInputGap, the 10 s contract); only a test sets it, to
	// prove the gate without waiting out the contract (export_test.go).
	emitGap time.Duration
}

// pollGap is the backup list-clients poll gap: the 2 s production value,
// or the test's override.
func (o *Options) pollGap() time.Duration {
	if o.inputPoll > 0 {
		return o.inputPoll
	}
	return inputPollGap
}

// inputEmitGap is the per-pane PaneInput gate: the 10 s contract value,
// or the test's override.
func (o *Options) inputEmitGap() time.Duration {
	if o.emitGap > 0 {
		return o.emitGap
	}
	return defaultInputGap
}

// readTimeout bounds each inventory read (defaults, fixed): a read that has
// not returned by then is abandoned and counted as result timeout. Only a
// test changes it (SetReadTimeoutForTest), to force timeouts fast.
var readTimeout = 10 * time.Second

// Observer finds the tmux servers in the socket folder, keeps one control
// client on each, reports each server's inventory in the Snapshot and
// emits the E1 and E3 lifecycle events (01 E1, E2, E3).
type Observer struct {
	opts    Options
	folder  string
	tel     *obsTelemetry
	servers map[string]*server
	order   []string
	rescan  chan struct{} // tokens for the watch goroutine (buffered 1)
}

// New checks opts and makes the clients; Run starts them.
func New(opts Options) (*Observer, error) {
	tel, err := newObsTelemetry(opts.Telemetry)
	if err != nil {
		return nil, err
	}
	o := &Observer{opts: opts, folder: opts.Folder, tel: tel, servers: map[string]*server{}, rescan: make(chan struct{}, 1)}
	if o.folder == "" {
		o.folder = SocketFolder(os.Getenv, os.Getuid())
	}
	// One parser Metrics for every client: the parse failures and the text
	// fields a read left blank (fleet_agent_tmux_inventory_fields_dropped_total)
	// reach the agent's meter and log only through it.
	var pm *tmux.Metrics
	if opts.Telemetry != nil {
		if pm, err = tmux.NewMetrics(opts.Telemetry); err != nil {
			return nil, err
		}
	}
	names := opts.Servers
	if names == nil {
		names = []string{"default", "fleet-probe"}
	}
	for _, name := range names {
		if _, ok := serverKinds[name]; !ok || o.servers[name] != nil {
			return nil, fmt.Errorf("inventory: server %q is not default, fleet or fleet-probe, or is named twice", name)
		}
		s := &server{
			o: o, name: name, kind: kindWord(name), dirty: make(chan struct{}, 1), snap: make(chan chan *agentv1.TmuxServer),
			actDirty: make(chan struct{}, 1), inDirty: make(chan struct{}, 1),
			track: newActivityTracker(time.Now, opts.IdleThreshold), inputs: newInputTracker(time.Now, opts.inputEmitGap()),
			avail:  newAvailOverlay(),
			winAct: map[string]time.Time{}, summaries: map[string]time.Time{}, pending: map[string]int{},
		}
		c, err := tmux.NewClient(tmux.ClientOptions{
			Program: opts.Program, Socket: filepath.Join(o.folder, name), Server: name,
			Runner: opts.Runner, Telemetry: opts.Telemetry, Notify: s.notify,
			Parser: tmux.Options{MaxReplyBytes: opts.maxReply, Metrics: pm},
		})
		if err != nil {
			return nil, err
		}
		s.c = c
		s.wake = &waker{c: c, now: time.Now}
		o.servers[name] = s
		o.order = append(o.order, name)
		tel.running(s.kind, false)
	}
	return o, nil
}

func kindWord(name string) string {
	if name == "fleet-probe" {
		return "probe"
	}
	return name
}

// Folder is the socket folder watched (status.json and doctor print it).
func (o *Observer) Folder() string { return o.folder }

// WatchOff reports that the folder watch could not be set or died:
// status.json then says tmux_watch: off.
func (o *Observer) WatchOff() bool { return o.tel.off.Load() }

// Client is the control client of a server ("default", "fleet-probe"), or
// nil: 7e and 7f subscribe and read through it.
func (o *Observer) Client(server string) *tmux.Client {
	if s := o.servers[server]; s != nil {
		return s.c
	}
	return nil
}

// Run watches the folder and runs every client and sync loop until ctx
// ends; it returns when all of them have.
func (o *Observer) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { o.watch(ctx) })
	for _, name := range o.order {
		s := o.servers[name]
		// Registered before Run: the client sends them at each attach. The
		// structure subscription is valid by construction, so it cannot be
		// refused here; a refused activity subscription is logged and the
		// observer runs without it (reads still evaluate activity).
		_ = s.c.Subscribe(ctx, structure)
		if err := s.c.Subscribe(ctx, activitySub); err != nil {
			o.tel.subscribeFailed(ctx, s.kind, activitySub.Name, err)
		}
		// A refused input subscription is logged and the observer runs
		// without it: the backup poll never confirms, so it keeps the
		// 2 s reads (a missed push cannot lose a key, 02h E13).
		if err := s.c.Subscribe(ctx, inputSub); err != nil {
			o.tel.subscribeFailed(ctx, s.kind, inputSub.Name, err)
		}
		wg.Go(func() { _ = s.c.Run(ctx) }) // ends with ctx, or ErrTooOld (its tmux.exit says so)
		wg.Go(func() { s.loop(ctx) })
	}
	wg.Wait()
}

// Snapshot is the conn Snapshot hook: it re-lists the socket folder (row-7
// ruling fork 4(b)), has each server's sync loop read its inventory now,
// and sets snap.TmuxServers. A server never seen is reported not running;
// one whose client is down keeps the last inventory with observed false,
// so a pane is never reported gone because the observer was.
func (o *Observer) Snapshot(ctx context.Context, snap *agentv1.Snapshot) error {
	o.askRescan()
	for _, name := range o.order { // the level carries fresh activity too
		select {
		case o.servers[name].actDirty <- struct{}{}:
		default:
		}
	}
	servers := make([]*agentv1.TmuxServer, 0, len(o.order))
	for _, name := range o.order {
		reply := make(chan *agentv1.TmuxServer, 1)
		select {
		case o.servers[name].snap <- reply:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case srv := <-reply:
			servers = append(servers, srv)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	snap.TmuxServers = servers
	return nil
}

// server is one watched socket: its client and its sync loop's state.
type server struct {
	o          *Observer
	name, kind string
	c          *tmux.Client
	wake       *waker
	dirty      chan struct{}                 // a notice came: read again (buffered 1)
	snap       chan chan *agentv1.TmuxServer // Snapshot requests
	actDirty   chan struct{}                 // window activity moved: refresh it (buffered 1)
	inDirty    chan struct{}                 // session activity moved: read the clients (buffered 1)

	// inMu guards inputs: the loop's syncInput and row 18's on-demand
	// CheckInput share the one tracker, and CheckInput runs on the
	// caller's goroutine. Everything else below stays owned by the loop
	// goroutine alone.
	inMu   sync.Mutex
	inputs *inputTracker

	// availMu guards the availability scaffold: the loop's sync and row
	// 18's ApplyAvailability share the overlay, the last synced start
	// and the last synced live pane ids. The loop never emits or reads
	// tmux while holding it.
	availMu    sync.Mutex
	avail      *availOverlay
	availStart string
	availLive  map[string]bool

	reads atomic.Int64 // inventory reads started (the pacing test counts them)

	// Owned by the loop goroutine alone.
	last   *agentv1.TmuxServer // nil: no server running (or none seen yet)
	known  bool                // a first state was read: events start after it
	failed bool                // a read failed before the first good state
	// pending maps a pane id read dead with no exit status to the
	// follow-up reads still owed it (deadStatusPollMax at most).
	pending map[string]int

	track     *activityTracker
	winAct    map[string]time.Time // window "@N" to its activity time
	summaries map[string]time.Time // pane "%N" to its latest E5 summary
}

// notify runs on the client's reader goroutine: it only drops tokens,
// never blocks and never calls the client.
func (s *server) notify(m tmux.Message) {
	if triggers(m) {
		select {
		case s.dirty <- struct{}{}:
		default:
		}
		if _, ok := m.(tmux.Exit); ok {
			s.o.askRescan() // fork 4(b): rescan after a control client exits
		}
	}
	if m, ok := m.(tmux.SubscriptionChanged); ok {
		var dst chan struct{}
		switch m.Name {
		case activitySub.Name:
			dst = s.actDirty
		case inputSub.Name:
			dst = s.inDirty
		}
		if dst != nil {
			select {
			case dst <- struct{}{}:
			default:
			}
		}
	}
	if s.o.opts.Notify != nil {
		s.o.opts.Notify(s.name, m)
	}
}

// level is what the Snapshot carries for this server.
func (s *server) level() *agentv1.TmuxServer {
	if s.last == nil {
		return &agentv1.TmuxServer{Kind: serverKinds[s.name], SocketName: s.name}
	}
	return proto.CloneOf(s.last)
}

// sync reads the server's state now and emits what changed. Each read is
// bounded at readTimeout: past it the read is abandoned and counted as a
// timeout, keeping the last good inventory. A context already spent is
// never issued as a command: the write would race the dead deadline and
// take the conn down with it.
func (s *server) sync(ctx context.Context) {
	switch s.c.State() {
	case tmux.StateUp:
		s.reads.Add(1)
		if ctx.Err() != nil {
			return // the waiter went away: count nothing, as before
		}
		rctx, cancel := context.WithTimeout(ctx, readTimeout)
		if rctx.Err() != nil {
			cancel()
			s.o.tel.readDone(ctx, s.kind, "timeout")
			s.o.tel.inventoryFailed(ctx, s.kind, rctx.Err())
			s.failed = true
			return
		}
		inv, err := s.c.ReadInventory(rctx)
		cancel()
		if errors.Is(err, tmux.ErrNotAttached) || ctx.Err() != nil {
			return // the client's state change wakes the loop
		}
		if err != nil {
			// The last good inventory stands, observed true while the
			// client is attached (README "When reads fail").
			s.o.tel.readDone(ctx, s.kind, readErrKind(err))
			s.o.tel.inventoryFailed(ctx, s.kind, err)
			s.failed = true
			return
		}
		s.o.tel.readDone(ctx, s.kind, "ok")
		if s.last == nil || s.last.GetServerStartId() != inv.StartID {
			s.track.reset() // tmux ids restart with a new server: reuse nothing
			s.inMu.Lock()
			s.inputs.reset()
			s.inMu.Unlock()
			s.availMu.Lock()
			s.avail.reset()
			s.availMu.Unlock()
			s.winAct = map[string]time.Time{}
			s.summaries = map[string]time.Time{}
		}
		s.refreshActivity(ctx, false)
		changes := s.track.evaluate(s.outlets(inv))
		next := buildServer(s.name, inv, s.c.Version())
		s.track.stamp(next)
		s.inMu.Lock()
		s.inputs.stamp(next)
		s.inMu.Unlock()
		s.availMu.Lock()
		live, sess := availLive(next)
		availEdges := s.avail.reconcile(live, sess, s.name, inv.StartID)
		s.avail.stamp(next)
		s.availStart, s.availLive = inv.StartID, live
		s.availMu.Unlock()
		s.apply(ctx, next)
		s.emitChanges(ctx, inv.StartID, changes)
		s.emit(ctx, availEdges...)
	case tmux.StateAbsent:
		s.track.reset()
		s.inMu.Lock()
		s.inputs.reset()
		s.inMu.Unlock()
		s.availMu.Lock()
		s.avail.reset()
		s.availStart, s.availLive = "", nil
		s.availMu.Unlock()
		s.winAct = map[string]time.Time{}
		s.summaries = map[string]time.Time{}
		s.apply(ctx, nil)
	default: // connecting, backing off, too old, stopped: not observed
		if s.last != nil && s.last.GetObserved() {
			down := proto.CloneOf(s.last)
			down.Observed = false
			s.last = down
			s.emit(ctx, serverEvent(agentv1.ChangeKind_CHANGE_KIND_UPDATED, down))
		}
	}
}

// apply stores next (nil: no server) and then emits the events from the
// last state to it. The first state read after the agent starts emits
// nothing: the connect Snapshot carries it (02i). But a first good read
// after reads failed emits CREATED with the full server, so the hub learns
// of a server whose reads failed from the start.
func (s *server) apply(ctx context.Context, next *agentv1.TmuxServer) {
	prev, known := s.last, s.known
	s.last, s.known = next, true
	s.o.tel.running(s.kind, next != nil)
	if next != nil && (prev == nil || prev.GetServerStartId() != next.GetServerStartId()) {
		s.o.tel.serverSeen(ctx, s.kind, next.GetServerStartId(), next.GetVersion())
	}
	if !known && (!s.failed || next == nil) {
		return
	}
	switch {
	case prev == nil && next == nil:
	case next == nil:
		s.clearDeadStatus() // the server is gone: nothing to wait for
		s.emit(ctx, closedServer(prev))
	case prev == nil:
		s.emit(ctx, serverEvent(agentv1.ChangeKind_CHANGE_KIND_CREATED, next))
	case prev.GetServerStartId() != next.GetServerStartId():
		// Pane ids restart with the server: a wait for the old %3 must
		// not settle on the new %3.
		s.clearDeadStatus()
		s.emit(ctx, closedServer(prev), serverEvent(agentv1.ChangeKind_CHANGE_KIND_CREATED, next))
	default:
		evs := diff(prev, next)
		s.emit(ctx, evs...)
		s.reapDeadStatus(ctx)
		s.watchDeadStatus(evs)
		if !prev.GetObserved() { // seen again: the level, after what changed meanwhile
			s.emit(ctx, serverEvent(agentv1.ChangeKind_CHANGE_KIND_UPDATED, next))
		}
	}
}

func (s *server) emit(ctx context.Context, evs ...*agentv1.Event) {
	if s.o.opts.Emitter == nil {
		return
	}
	for _, ev := range evs {
		// A failure is the emitter's to count and log (ring.emit_failed,
		// counted as loss, so the hub asks for a snapshot).
		_, _ = s.o.opts.Emitter.Emit(ctx, ev)
	}
}

// serverEvent carries the server after the change, with its sessions.
func serverEvent(kind agentv1.ChangeKind, srv *agentv1.TmuxServer) *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_TmuxServer{TmuxServer: &agentv1.TmuxServerEvent{Kind: kind, Server: srv}}}
}

// closedServer says a server start is gone, and with it all its panes.
func closedServer(prev *agentv1.TmuxServer) *agentv1.Event {
	return serverEvent(agentv1.ChangeKind_CHANGE_KIND_CLOSED, &agentv1.TmuxServer{
		Kind: prev.GetKind(), SocketName: prev.GetSocketName(), ServerStartId: prev.GetServerStartId(), Version: prev.GetVersion(),
	})
}
