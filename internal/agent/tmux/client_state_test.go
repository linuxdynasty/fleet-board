package tmux_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// Changed returns the state now and a channel closed at the next change,
// so a watcher (7d2's inventory observer) follows the client with no
// timer; Version is the attached server's version ("" before an attach).
func TestChangedAndVersion(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)
			st, changed := r.Changed()
			if st != tmux.StateUp {
				t.Fatalf("Changed state = %s, want up", st)
			}
			if v := r.Version(); !regexp.MustCompile(`^3\.[0-9]+[a-z]?$`).MatchString(v) {
				t.Fatalf("Version = %q", v)
			}
			select {
			case <-changed:
				t.Fatal("changed closed with no change")
			default:
			}
			srv.Kill(t)
			select {
			case <-changed:
			case <-time.After(wait):
				t.Fatal("changed not closed after the server died")
			}
			if st, _ := r.Changed(); st == tmux.StateUp {
				t.Fatal("still up after the server died")
			}
		})
	}
}

func TestVersionBeforeAttach(t *testing.T) {
	c, err := tmux.NewClient(tmux.ClientOptions{Program: "tmux", Socket: "/nonexistent/s", Server: "fleet", Runner: mustRunner(t)})
	if err != nil {
		t.Fatal(err)
	}
	if v := c.Version(); v != "" {
		t.Fatalf("Version before attach = %q", v)
	}
}
