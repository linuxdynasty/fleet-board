package tmux_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// wait is the bound for anything a test waits on: generous, so a loaded
// machine does not flake; a healthy run takes well under a second per step.
const wait = 30 * time.Second

// notes collects what the client's Notify hands over.
type notes struct {
	mu    sync.Mutex
	msgs  []tmux.Message
	added chan struct{}
	// stall, when set, is called on the reader goroutine before a message
	// is kept: a test uses it to stop the reader.
	stall func(tmux.Message)
}

func newNotes() *notes { return &notes{added: make(chan struct{}, 1)} }

func (n *notes) notify(m tmux.Message) {
	if n.stall != nil {
		n.stall(m)
	}
	n.mu.Lock()
	n.msgs = append(n.msgs, m)
	n.mu.Unlock()
	select {
	case n.added <- struct{}{}:
	default:
	}
}

func (n *notes) all() []tmux.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]tmux.Message(nil), n.msgs...)
}

// waitFor waits until some message from index from on satisfies ok and
// returns it with the index after it. ok is called once per message, in
// order.
func (n *notes) waitFor(t *testing.T, from int, what string, ok func(tmux.Message) bool) (tmux.Message, int) {
	t.Helper()
	deadline := time.After(wait)
	i := from // ok sees each message once: it may keep state
	for {
		msgs := n.all()
		for ; i < len(msgs); i++ {
			if ok(msgs[i]) {
				return msgs[i], i + 1
			}
		}
		select {
		case <-n.added:
		case <-deadline:
			t.Fatalf("no %s within %s", what, wait)
		}
	}
}

// running is a client whose Run loop the test owns.
type running struct {
	*tmux.Client
	notes *notes
	logs  *lockedBuffer
	tel   *telemetry.Telemetry
	done  chan error
}

// startClient makes a client for srv (server name "fleet": a throwaway
// server stands in for it) and runs it until the test ends.
func startClient(t *testing.T, srv *tmuxtest.Server, n *notes, edit ...func(*tmux.ClientOptions)) *running {
	t.Helper()
	return startClientFor(t, srv.Bin.Path, srv.Socket, n, edit...)
}

func startClientFor(t *testing.T, program, socket string, n *notes, edit ...func(*tmux.ClientOptions)) *running {
	t.Helper()
	return startClientWith(t, program, socket, n, nil, edit...)
}

// startClientWith is startClientFor with a hook that runs on the client
// before Run starts.
func startClientWith(t *testing.T, program, socket string, n *notes, before func(*tmux.Client), edit ...func(*tmux.ClientOptions)) *running {
	t.Helper()
	if n == nil {
		n = newNotes()
	}
	tel, logs := newTelemetry(t)
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := tmux.ClientOptions{
		Program:   program,
		Socket:    socket,
		Server:    "fleet",
		Runner:    runner,
		Telemetry: tel,
		Notify:    n.notify,
	}
	for _, e := range edit {
		e(&opts)
	}
	c, err := tmux.NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	if before != nil {
		before(c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{Client: c, notes: n, logs: logs, tel: tel, done: make(chan error, 1)}
	go func() { r.done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-r.done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, tmux.ErrTooOld) {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(wait):
			t.Error("Run did not return after its context ended")
		}
	})
	return r
}

func (r *running) waitState(t *testing.T, s tmux.State) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := r.WaitState(ctx, s); err != nil {
		t.Fatalf("waiting for state %s (now %s): %v", s, r.State(), err)
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	t.Cleanup(cancel)
	return ctx
}

// paneKey is the PaneKey of pane on the server the client is attached to.
func (r *running) paneKey(pane tmux.PaneID) *typev1.PaneKey {
	return &typev1.PaneKey{Server: "fleet", ServerStartId: r.ServerStartID(), PaneId: pane.String()}
}

func literal(t *testing.T, key *typev1.PaneKey, text string) typing.Line {
	t.Helper()
	l, err := typing.NewLiteral(nil, key, typing.PurposeOperatorText, text)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func keys(t *testing.T, key *typev1.PaneKey, k ...typing.Key) typing.Line {
	t.Helper()
	l, err := typing.NewKeys(nil, key, typing.PurposeOperatorText, k...)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// onlyPane returns the one pane of the session named by window id order:
// the first pane ListPanes reports in session sess.
func firstPane(t *testing.T, r *running, sess tmux.SessionID) tmux.Pane {
	t.Helper()
	panes, err := r.ListPanes(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range panes {
		if p.Session == sess {
			return p
		}
	}
	t.Fatalf("no pane in session %s: %+v", sess, panes)
	return tmux.Pane{}
}
