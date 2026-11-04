package main

import (
	"context"
	"testing"
)

// TestAttachWireWithoutTmux: with no observer every seam is nil-safe and
// reports nothing: no handlers, no open attaches, and the shutdown sweep
// is a no-op.
func TestAttachWireWithoutTmux(t *testing.T) {
	t.Parallel()
	att, err := startAttach(context.Background(), tmuxRun{}, nil, nil)
	if err != nil {
		t.Fatalf("startAttach without tmux = %v, want nil", err)
	}
	if att != nil {
		t.Fatalf("startAttach without tmux = %+v, want nil", att)
	}
	if hs := att.handlers(); len(hs) != 0 {
		t.Fatalf("handlers = %d, want none", len(hs))
	}
	if n := att.openCount(); n != 0 {
		t.Fatalf("openCount = %d, want 0", n)
	}
	att.dropAll()
}
