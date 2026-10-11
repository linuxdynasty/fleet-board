package tmuxtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
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

// Run returns tmux's whole stdout, also past the 1 KiB a one-shot `tmux -V`
// needs: the rig's commands run under the measured `tmux` caller (16 KiB).
func TestRunReturnsAReplyOverOneKiB(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			big := strings.Repeat("x", 4000)
			srv.Run(t, "set-option", "-g", "@big", big)
			if got := srv.Run(t, "show-options", "-gv", "@big"); got != big+"\n" {
				t.Fatalf("show-options returned %d bytes, want %d", len(got), len(big)+1)
			}
		})
	}
}

// A test binary that died before its cleanup (a panic, a kill) leaves its
// server running. Sweep, which Main runs first, kills the servers whose
// recorded test process is gone and removes their folders; a server whose
// test process is alive (this one, or another package's running now) stays.
func TestSweepKillsOnlyServersWhoseTestProcessIsGone(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			live := tmuxtest.Start(t, b, "-s", "live", "exec cat")
			orphan := tmuxtest.Start(t, b, "-s", "orphan", "exec cat")
			serverPid, err := strconv.Atoi(strings.TrimSpace(orphan.Run(t, "display-message", "-p", "#{pid}")))
			if err != nil {
				t.Fatal(err)
			}
			owner := filepath.Join(orphan.Dir, tmuxtest.OwnerFile)
			if _, err := os.Stat(owner); err != nil {
				t.Fatalf("Start wrote no owner file: %v", err)
			}
			if err := os.WriteFile(owner, []byte(fmt.Sprintf("%d\n", deadPid(t, b))), 0o600); err != nil {
				t.Fatal(err)
			}
			tmuxtest.Sweep()
			// The folder (and so the socket) is gone, so has-session cannot
			// tell; the server's own pid must be gone.
			gone := time.Now().Add(10 * time.Second)
			for syscall.Kill(serverPid, 0) == nil {
				if time.Now().After(gone) {
					_ = syscall.Kill(serverPid, syscall.SIGKILL)
					t.Fatal("Sweep left the orphaned server running")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if _, err := os.Stat(orphan.Dir); !os.IsNotExist(err) {
				t.Fatalf("Sweep left the orphan's folder: %v", err)
			}
			if !live.Alive() {
				t.Fatal("Sweep killed a server whose test process is alive")
			}
		})
	}
}

// deadPid is the pid of a process that ran and was reaped.
func deadPid(t *testing.T, b tmuxtest.Binary) int {
	t.Helper()
	r, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), proc.Spec{Caller: c, Path: b.Path, Args: []string{"-V"}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Pid
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

// A Sibling is a second server in the same folder on its own socket; both
// run at once and the Sibling is killed when its test ends.
func TestSiblingIsASecondServerInTheFolder(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			tmuxtest.Need(t, b)
			var sib *tmuxtest.Server
			t.Run("start", func(t *testing.T) {
				srv := tmuxtest.Start(t, b, "-s", "one", "exec cat")
				sib = srv.Sibling(t, "default", "-s", "two", "exec cat")
				if sib.Socket != filepath.Join(srv.Dir, "default") || !srv.Alive() || !sib.Alive() {
					t.Fatalf("sibling %s alive %v, first alive %v", sib.Socket, sib.Alive(), srv.Alive())
				}
				if got := strings.TrimSpace(sib.Run(t, "list-sessions", "-F", "#{session_name}")); got != "two" {
					t.Fatalf("sibling sessions = %q", got)
				}
			})
			if sib == nil {
				t.Fatal("the start subtest made no sibling")
			}
			if sib.Alive() {
				t.Fatal("sibling still alive after its test ended")
			}
		})
	}
}
