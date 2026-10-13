package tmux_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

func paneIDOf(t *testing.T, id string) tmux.PaneID {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(id), "%"))
	if err != nil || n < 0 {
		t.Fatalf("pane id = %q: %v", id, err)
	}
	return tmux.PaneID(n)
}

// A pane's working folder and pid read through the control client, so the
// observed-goal tracker learns them without a second tmux.
func TestClientPaneLocation(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			dir := t.TempDir()
			out := srv.Run(t, "new-window", "-P", "-F", "#{pane_id}", "-c", dir, "-t", "work", "exec sleep 100")
			id := paneIDOf(t, out)

			ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			got, err := r.PaneLocation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != want {
				t.Fatalf("path = %q, want %q", got.Path, want)
			}
			if got.PID <= 0 {
				t.Fatalf("pid = %d, want the pane's process", got.PID)
			}
			if _, err := r.PaneLocation(ctx, tmux.PaneID(999999)); err == nil {
				t.Fatal("a missing pane locates without an error")
			}
		})
	}
}
