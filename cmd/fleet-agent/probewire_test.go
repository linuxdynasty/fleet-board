package main

import (
	"context"
	"sync"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
)

type probeFakeEmitter struct{}

func (probeFakeEmitter) Emit(ctx context.Context, ev *agentv1.Event) (uint64, error) {
	return 0, nil
}

// The manager builds with a tmux program, a socket and the fleet-owned
// probe folder, and its handlers are the two probe commands.
func TestProbeManagerBuildsWithItsHandlers(t *testing.T) {
	t.Parallel()
	mgr, err := buildProbeManager(nil, probeFakeEmitter{}, "tmux", "/tmp/fleet-probe-test-s", t.TempDir())
	if err != nil {
		t.Fatalf("buildProbeManager: %v", err)
	}
	p := &probeRun{mgr: mgr}
	hs := p.handlers()
	if len(hs) != 2 {
		t.Fatalf("handlers = %d, want EnsureProbe and StopProbe", len(hs))
	}
	kinds := map[string]bool{}
	for _, h := range hs {
		kinds[h.Kind()] = true
	}
	if !kinds["EnsureProbe"] || !kinds["StopProbe"] {
		t.Errorf("handler kinds = %v, want EnsureProbe and StopProbe", kinds)
	}
	wantCaps := map[string]bool{
		"usage.probe.claude": false, "usage.probe.codex": false,
		"usage.probe.muse": false, "usage.probe.cursor": false,
	}
	for _, h := range hs {
		for _, c := range h.Capabilities() {
			if _, ok := wantCaps[c.GetName()]; ok && c.GetVersion() == 1 {
				wantCaps[c.GetName()] = true
			}
		}
	}
	for name, seen := range wantCaps {
		if !seen {
			t.Errorf("no handler advertises %s at version 1", name)
		}
	}
}

// The build fails fast on a half-built setup: no tmux program, no socket,
// no probe folder, no emitter.
func TestProbeManagerBuildRequires(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name, program, socket, folder string
		em                            probe.Emitter
	}{
		{"no tmux program", "", "/tmp/s", dir, probeFakeEmitter{}},
		{"no socket", "tmux", "", dir, probeFakeEmitter{}},
		{"no probe folder", "tmux", "/tmp/s", "", probeFakeEmitter{}},
		{"no emitter", "tmux", "/tmp/s", dir, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := buildProbeManager(nil, tc.em, tc.program, tc.socket, tc.folder); err == nil {
				t.Errorf("buildProbeManager with %s accepted", tc.name)
			}
		})
	}
}

// Without tmux there is nowhere to keep a probe session, so the run goes
// on with no probe manager and no handlers.
func TestStartProbeWithoutTmux(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	p, err := startProbe(context.Background(), &wg, nil, nil, tmuxRun{})
	if err != nil {
		t.Fatalf("startProbe without tmux: %v", err)
	}
	if p != nil {
		t.Errorf("startProbe without tmux = %v, want nil", p)
	}
	if hs := p.handlers(); len(hs) != 0 {
		t.Errorf("nil probe handlers = %d, want none", len(hs))
	}
	wg.Wait()
}

// With tmux the run starts the manager until the context ends.
func TestStartProbeRunsUntilTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	p, err := startProbe(ctx, &wg, nil, probeFakeEmitter{}, tmuxRun{
		obs: &inventory.Observer{}, program: "tmux",
	})
	if err != nil {
		t.Fatalf("startProbe: %v", err)
	}
	if p == nil || p.mgr == nil {
		t.Fatal("startProbe with tmux = nil, want a manager")
	}
	if hs := p.handlers(); len(hs) != 2 {
		t.Errorf("handlers = %d, want EnsureProbe and StopProbe", len(hs))
	}
	cancel()
	wg.Wait()
}
