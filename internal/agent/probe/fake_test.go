package probe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// errNotFound stands in for a missing harness binary on PATH.
var errNotFound = errors.New("not found")

// fakeBackend is the probe's tmux surface in tests: scripted screens,
// a log of every key the pass sends, and a record of sessions. It fails
// the test if two calls overlap, proving passes never run concurrently.
type fakeBackend struct {
	tb testing.TB

	mu       sync.Mutex
	inFlight int
	sessions map[string]string // name -> binary it was created with
	dirs     map[string]string // name -> dir it was created in
	captures []string          // screens returned in order
	sent     []string          // "literal <text>" and "key Enter" entries
	killed   []string
	dead     map[string]bool
	commands map[string]string // pane command by session

	captureErr error
}

func newFakeBackend(tb testing.TB) *fakeBackend {
	tb.Helper()
	return &fakeBackend{
		tb:       tb,
		sessions: map[string]string{},
		dirs:     map[string]string{},
		dead:     map[string]bool{},
		commands: map[string]string{},
	}
}

func (f *fakeBackend) enter() func() {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > 1 {
		f.tb.Errorf("probe backend calls overlap: %d in flight", f.inFlight)
	}
	return func() {
		f.inFlight--
		f.mu.Unlock()
	}
}

func (f *fakeBackend) HasSession(_ context.Context, name string) (bool, error) {
	defer f.enter()()
	_, ok := f.sessions[name]
	return ok, nil
}

func (f *fakeBackend) NewSession(_ context.Context, name, dir, binary string) error {
	defer f.enter()()
	if _, ok := f.sessions[name]; ok {
		return fmt.Errorf("duplicate session: %s", name)
	}
	f.sessions[name] = binary
	f.dirs[name] = dir
	f.commands[name] = binary
	return nil
}

func (f *fakeBackend) KillSession(_ context.Context, name string) error {
	defer f.enter()()
	delete(f.sessions, name)
	f.killed = append(f.killed, name)
	return nil
}

func (f *fakeBackend) Capture(_ context.Context, _ string) (string, error) {
	defer f.enter()()
	if f.captureErr != nil {
		return "", f.captureErr
	}
	if len(f.captures) == 0 {
		return "", fmt.Errorf("fake backend: no screen scripted")
	}
	s := f.captures[0]
	f.captures = f.captures[1:]
	return s, nil
}

func (f *fakeBackend) SendCommand(_ context.Context, _ string, text string) error {
	defer f.enter()()
	f.sent = append(f.sent, "literal "+text, "key Enter")
	return nil
}

func (f *fakeBackend) PaneDead(_ context.Context, name string) (bool, error) {
	defer f.enter()()
	return f.dead[name], nil
}

func (f *fakeBackend) PaneCommand(_ context.Context, name string) (string, error) {
	defer f.enter()()
	if c, ok := f.commands[name]; ok {
		return c, nil
	}
	return "sh", nil
}

func (f *fakeBackend) ServerStartID(_ context.Context) (string, error) {
	defer f.enter()()
	return "test-start", nil
}

func (f *fakeBackend) FirstPane(_ context.Context, name string) (string, error) {
	defer f.enter()()
	// No pane before the session exists: the pass must read the key
	// again after ensuring it.
	if _, ok := f.sessions[name]; !ok {
		return "", nil
	}
	return "%0", nil
}

// fakeEmitter records the events a pass emits, by body kind.
type fakeEmitter struct {
	mu     sync.Mutex
	events []*agentv1.Event
}

func (f *fakeEmitter) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return uint64(len(f.events)), nil
}

func (f *fakeEmitter) health() []*agentv1.ProbeHealth {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentv1.ProbeHealth
	for _, ev := range f.events {
		if h, ok := ev.GetBody().(*agentv1.Event_ProbeHealth); ok {
			out = append(out, h.ProbeHealth)
		}
	}
	return out
}

func (f *fakeEmitter) readings() []*agentv1.UsageReading {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentv1.UsageReading
	for _, ev := range f.events {
		if r, ok := ev.GetBody().(*agentv1.Event_UsageReading); ok {
			out = append(out, r.UsageReading)
		}
	}
	return out
}

// stepClock returns a Now func that moves one second per call, so attempt
// and success times order deterministically.
func stepClock() func() time.Time {
	base := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	n := 0
	var mu sync.Mutex
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
}

func testOptions(be *fakeBackend, em *fakeEmitter) Options {
	return Options{
		Backend:          be,
		Events:           em,
		ProbeDir:         "/probe",
		Now:              stepClock(),
		LookPath:         func(name string) (string, error) { return "/bin/" + name, nil },
		EnsureTries:      1,
		SettlePolls:      2,
		SettleEvery:      time.Millisecond,
		DefaultInterval:  10 * time.Minute,
		EnsureRetryEvery: time.Millisecond,
	}
}

func ensureCommand(probeID, accountID, accountKey, harness string, runNow bool) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_EnsureProbe{EnsureProbe: &agentv1.EnsureProbe{
		ProbeId:    &typev1.ProbeId{Value: probeID},
		AccountId:  &typev1.AccountId{Value: accountID},
		AccountKey: &typev1.AccountKey{Value: accountKey},
		Harness:    &typev1.Harness{Name: harness},
		RunNow:     runNow,
	}}}
}

func stopCommand(probeID string) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_StopProbe{StopProbe: &agentv1.StopProbe{
		ProbeId: &typev1.ProbeId{Value: probeID},
	}}}
}

func loadFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the test's own fixture paths under the repo
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return string(b)
}
