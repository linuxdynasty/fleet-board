package tmuxtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

func TestMain(m *testing.M) { os.Exit(tmuxtest.Main(m)) }

// The whole binary runs with TMUX unset and TMUX_TMPDIR an empty folder of
// its own, so no test can reach the developer's server by mistake.
func TestMainHidesTheDevelopersServer(t *testing.T) {
	if v, ok := os.LookupEnv("TMUX"); ok {
		t.Fatalf("TMUX is set in the test binary: %q", v)
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" || !strings.Contains(filepath.Base(dir), "fbm") {
		t.Fatalf("TMUX_TMPDIR = %q, want the folder tmuxtest.Main made", dir)
	}
	if _, err := os.Stat(dir); err != nil { //nolint:gosec // G703: the folder tmuxtest.Main made
		t.Fatalf("TMUX_TMPDIR folder: %v", err)
	}
}

func TestServerStartsInItsOwnShortFolderAndIsGoneAfterTheTest(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			tmuxtest.Need(t, b)
			var srv *tmuxtest.Server
			t.Run("inner", func(t *testing.T) {
				srv = tmuxtest.Start(t, b, "-s", "work", "-x", "100", "-y", "30", "exec cat")
				if len(srv.Socket) > 100 {
					t.Fatalf("socket path is %d bytes: %s", len(srv.Socket), srv.Socket)
				}
				if !strings.HasPrefix(srv.Socket, srv.Dir+string(filepath.Separator)) {
					t.Fatalf("socket %s is not in the server folder %s", srv.Socket, srv.Dir)
				}
				if got := srv.Run(t, "list-sessions", "-F", "#{session_name}"); got != "work\n" {
					t.Fatalf("list-sessions = %q, want work", got)
				}
				// The server read no config: an option a config could set has
				// tmux's built-in default.
				if got := srv.Run(t, "show-options", "-gv", "history-limit"); got != "2000\n" {
					t.Fatalf("history-limit = %q, want the built-in 2000 (-f /dev/null)", got)
				}
				for _, kv := range srv.Env() {
					if strings.HasPrefix(kv, "TMUX=") {
						t.Fatalf("Env holds %s", kv)
					}
				}
				argv := srv.Argv("attach")
				want := []string{b.Path, "-S", srv.Socket, "-f", "/dev/null", "attach"}
				if strings.Join(argv, " ") != strings.Join(want, " ") {
					t.Fatalf("Argv = %q, want %q", argv, want)
				}
			})
			if srv.Alive() {
				t.Fatal("the server outlived the test that started it")
			}
			if _, err := os.Stat(srv.Dir); !os.IsNotExist(err) {
				t.Fatalf("server folder %s still exists: %v", srv.Dir, err)
			}
		})
	}
}

func TestKillThenNewSessionStartsANewServerOnTheSameSocket(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "one", "exec cat")
			pid1 := srv.Run(t, "display-message", "-p", "#{pid}")
			srv.Kill(t)
			if srv.Alive() {
				t.Fatal("alive after Kill")
			}
			srv.NewSession(t, "-s", "two", "exec cat")
			if pid2 := srv.Run(t, "display-message", "-p", "#{pid}"); pid2 == pid1 {
				t.Fatalf("same server pid %s after a restart", pid1)
			}
		})
	}
}
