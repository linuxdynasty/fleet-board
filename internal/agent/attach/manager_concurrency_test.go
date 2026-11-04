package attach

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// openTestManager builds a manager against the fake tmux with the caller's
// dial, for tests that script the Terminal stream (a parked Dial, a slow
// browser).
func openTestManager(t *testing.T, program string, dial func(ctx context.Context) (Stream, error)) *Manager {
	t.Helper()
	caller, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), Options{
		Program: program, Runner: runner, Caller: caller,
		ServerStart: func(string) (string, bool) { return "start-1", true },
		Sockets: func(k typev1.TmuxServerKind) (string, string, bool) {
			if k == typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT {
				return "/fake/socket", "default", true
			}
			return "", "", false
		},
		Dial: dial,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestOpenCloseRaceLosesCleanly: an Open blocked in Dial loses to a Close,
// tears down only its own view and client, and leaves a retry working. The
// loser must never finish(id): the id is already gone (a finish would leak
// the loser's view) or names a retry (a finish would kill the winner).
func TestOpenCloseRaceLosesCleanly(t *testing.T) {
	t.Parallel()
	program, argvFile := fakeTmux(t)
	// Every Dial parks on its own release channel, so the test can
	// interleave a close and a retry around one stuck open.
	dialBlock := make(chan chan struct{}, 8)
	m := openTestManager(t, program, func(ctx context.Context) (Stream, error) {
		rel := make(chan struct{})
		dialBlock <- rel
		select {
		case <-rel:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &fakeStream{recv: make(chan *agentv1.TerminalResponse, 16), dialCtx: ctx}, nil
	})
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "race")
	type opened struct {
		view string
		err  error
	}
	open1Done := make(chan opened, 1)
	go func() {
		view, err := m.Open(ctx, openCmd("race", "work", 80, 24, false))
		open1Done <- opened{view, err}
	}()
	var rel1, rel2 chan struct{}
	select {
	case rel1 = <-dialBlock:
	case <-time.After(30 * time.Second):
		t.Fatal("first open never reached dial")
	}
	// The first open is past land (its view and client live) and parked
	// in Dial. Close it, then retry the same id while the loser is still
	// parked: the loser must not delete the winner.
	m.Close("race")
	open2Done := make(chan opened, 1)
	go func() {
		view, err := m.Open(ctx, openCmd("race", "work", 80, 24, false))
		open2Done <- opened{view, err}
	}()
	select {
	case rel2 = <-dialBlock:
	case <-time.After(30 * time.Second):
		t.Fatal("retry open never reached dial")
	}
	close(rel1)
	select {
	case o := <-open1Done:
		if !errors.Is(o.err, ErrServerDown) {
			t.Fatalf("loser open = %v, want ErrServerDown", o.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("loser open never returned")
	}
	// The winner survived the loser: still live, not finished out from
	// under it.
	if ids := m.OpenIDs(); len(ids) != 1 || ids[0] != "race" {
		t.Fatalf("live during the race = %v, want [race]", ids)
	}
	close(rel2)
	select {
	case o := <-open2Done:
		if o.err != nil {
			t.Fatalf("retry open = %v", o.err)
		}
		if o.view != "fleet-view-race" {
			t.Fatalf("retry view = %q, want fleet-view-race", o.view)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("retry open never returned")
	}
	killed := false
	for _, line := range argvLines(t, argvFile) {
		if strings.Contains(line, "kill-session") && strings.Contains(line, "fleet-view-race") {
			killed = true
		}
	}
	if !killed {
		t.Fatalf("no kill-session for fleet-view-race in %q", argvLines(t, argvFile))
	}
}

// overlapStream wraps a stream and reports concurrent Sends: the detector
// sleeps inside Send so two Sends that share the wire overlap for sure.
type overlapStream struct {
	inner  *fakeStream
	active atomic.Int32
	raced  atomic.Bool
}

func (s *overlapStream) Send(req *agentv1.TerminalRequest) error {
	if s.active.Add(1) != 1 {
		s.raced.Store(true)
	}
	defer s.active.Add(-1)
	time.Sleep(20 * time.Millisecond)
	return s.inner.Send(req)
}

func (s *overlapStream) Receive() (*agentv1.TerminalResponse, error) { return s.inner.Receive() }

// TestCloseDuringSlowSendDeliversBoth: the out pump is parked in a slow
// Send when Close reports beside it. Both messages must land, and the two
// Sends must never share the wire: one slow browser must not scramble the
// stream.
func TestCloseDuringSlowSendDeliversBoth(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	fs := &fakeStream{recv: make(chan *agentv1.TerminalResponse, 16), dialCtx: context.Background()}
	race := &overlapStream{inner: fs}
	m := openTestManager(t, program, func(context.Context) (Stream, error) { return race, nil })
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "slow")
	if _, err := m.Open(ctx, openCmd("slow", "work", 80, 24, false)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Ready and the banner are through: now the browser stalls, holding
	// every next Send.
	waitFor(t, "banner", 10*time.Second, func() bool {
		return strings.Contains(joinedOutputs(fs), "BANNER")
	})
	fs.sendGate = make(chan struct{})
	// A keystroke echoes off the fake client, so the out pump parks in
	// Send behind the gate.
	fs.recv <- down("slow", withInput([]byte("SLOW-1\n")))
	time.Sleep(time.Second)
	m.Close("slow")
	// The closed report parks beside the output; open the gate and both
	// must land, serialized.
	time.Sleep(200 * time.Millisecond)
	close(fs.sendGate)
	landed := func() (output, closed bool) {
		for _, up := range fs.ups() {
			if strings.Contains(string(up.GetUp().GetOutput()), "SLOW-1") {
				output = true
			}
			if up.GetUp().GetClosed() != nil {
				closed = true
			}
		}
		return output, closed
	}
	waitFor(t, "both messages", 10*time.Second, func() bool {
		output, closed := landed()
		return output && closed
	})
	if race.raced.Load() {
		t.Fatal("two Sends shared the wire")
	}
	var output, closed bool
	for _, up := range fs.ups() {
		if strings.Contains(string(up.GetUp().GetOutput()), "SLOW-1") {
			output = true
		}
		if up.GetUp().GetClosed() != nil {
			closed = true
		}
	}
	if !output || !closed {
		t.Fatalf("landed output=%v closed=%v, want both true", output, closed)
	}
	if ids := m.OpenIDs(); len(ids) != 0 {
		t.Fatalf("OpenIDs = %q, want none", ids)
	}
}
