package tmuxtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// The environment variables that locate tmux and say whether a missing tmux
// fails the run.
const (
	// EnvTmux32 is the path of a tmux 3.2 build (CI: 3.2a), the oldest
	// release the agent supports.
	EnvTmux32 = "FLEET_TMUX_32"
	// EnvTmuxLatest is the path of the latest tmux release. Unset, the tmux
	// on PATH stands in for it.
	EnvTmuxLatest = "FLEET_TMUX_LATEST"
	// EnvRequire set to 1 turns a missing tmux from a skip into a failure
	// (CI sets it).
	EnvRequire = "FLEET_REQUIRE_TMUX"
)

// maxSocketPath keeps the socket path under the 104-byte sun_path of macOS
// (108 on Linux), with room to spare.
const maxSocketPath = 96

// Binary is one tmux program a test runs against.
type Binary struct {
	// Name is "3.2" or "latest": the subtest name.
	Name string
	// Path is the program; empty when it is not installed.
	Path string

	why, env string // why Path is empty, and the variable that sets it
}

// Main is the body of a TestMain for every package that tests tmux: it
// points TMUX_TMPDIR at a new empty folder and unsets TMUX for the whole
// test binary, so even a test that forgot -S cannot reach the developer's
// own server, then runs the tests and removes the folder.
//
//	func TestMain(m *testing.M) { os.Exit(tmuxtest.Main(m)) }
func Main(m *testing.M) int {
	dir, err := shortTempDir("fbm")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Setenv("TMUX_TMPDIR", dir); err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		return 1
	}
	if err := os.Unsetenv("TMUX"); err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		return 1
	}
	return m.Run()
}

// Binaries returns the two tmux releases a test runs against, 3.2 and
// latest, as a test names its subtests. Path is empty for one that is not
// installed; Start (or Need) then skips the test, or fails it when
// FLEET_REQUIRE_TMUX=1.
//
//	for _, b := range tmuxtest.Binaries() {
//		t.Run(b.Name, func(t *testing.T) { srv := tmuxtest.Start(t, b, "-s", "work", "exec cat"); ... })
//	}
func Binaries() []Binary {
	out := make([]Binary, 0, 2)
	for _, b := range []struct{ name, env string }{{"3.2", EnvTmux32}, {"latest", EnvTmuxLatest}} {
		path, why := locate(b.env)
		out = append(out, Binary{Name: b.name, Path: path, why: why, env: b.env})
	}
	return out
}

// Need skips the test when b is not installed, or fails it when
// FLEET_REQUIRE_TMUX=1.
func Need(tb testing.TB, b Binary) {
	tb.Helper()
	if b.Path != "" {
		return
	}
	if os.Getenv(EnvRequire) == "1" {
		tb.Fatalf("tmux %s is required (%s=1): %s", b.Name, EnvRequire, b.why)
	}
	tb.Skipf("tmux %s not found (%s); set %s to run these tests", b.Name, b.why, b.env)
}

// locate returns the program the variable names, or for the latest release
// the tmux on PATH; else "" and why.
func locate(env string) (string, string) {
	if p := os.Getenv(env); p != "" {
		if !isExecutable(p) {
			return "", env + " is not an executable file"
		}
		return p, ""
	}
	if env != EnvTmuxLatest {
		return "", env + " is not set"
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if p := filepath.Join(dir, "tmux"); dir != "" && isExecutable(p) {
			return p, ""
		}
	}
	return "", env + " is not set and no tmux is on PATH"
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p) //nolint:gosec // G703: a path the test runner named (FLEET_TMUX_*) or a PATH entry
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Server is one throwaway tmux server: its own socket in a short new
// folder, no config file (-f /dev/null) and an environment without TMUX.
// Cleanup kills it and fails the test if has-session still finds it.
type Server struct {
	Bin Binary
	// Dir is the server's folder (its TMUX_TMPDIR); Socket is Dir/s.
	Dir    string
	Socket string

	runner *proc.Runner
	caller proc.Caller
	env    []string
}

// Start makes a folder and a server in it with one detached session:
// new-session -d and then args (for example "-s", "work", "-x", "100",
// "-y", "30", "exec cat"). The server is killed when the test ends.
func Start(tb testing.TB, b Binary, args ...string) *Server {
	tb.Helper()
	Need(tb, b)
	dir, err := shortTempDir("fbt")
	if err != nil {
		tb.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		tb.Fatal(err)
	}
	caller, err := proc.For("tmux")
	if err != nil {
		tb.Fatal(err)
	}
	s := &Server{Bin: b, Dir: dir, Socket: filepath.Join(dir, "s"), runner: runner, caller: caller}
	s.env = cleanEnv(dir)
	tb.Cleanup(func() {
		s.Kill(tb)
		_ = os.RemoveAll(dir)
	})
	s.NewSession(tb, args...)
	return s
}

// NewSession runs new-session -d with args. After Kill it starts a new
// server on the same socket.
func (s *Server) NewSession(tb testing.TB, args ...string) {
	tb.Helper()
	s.Run(tb, append([]string{"new-session", "-d"}, args...)...)
}

// Run runs one tmux command against this server and returns its stdout. A
// failure fails the test.
func (s *Server) Run(tb testing.TB, args ...string) string {
	tb.Helper()
	out, err := s.Try(args...)
	if err != nil {
		tb.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// Try runs one tmux command against this server and returns its stdout or
// an error with its stderr.
func (s *Server) Try(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := s.runner.Run(ctx, proc.Spec{Caller: s.caller, Path: s.Bin.Path, Args: s.Argv(args...)[1:], Env: s.env})
	if err != nil {
		return string(res.Stdout), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(res.Stderr)))
	}
	return string(res.Stdout), nil
}

// Argv is the whole command line for this server: the program, -S, -f
// /dev/null, then args. A test file that starts a long-lived client (the
// PTY "person at the keyboard") uses it with Env; test files may start
// processes, this package may not.
func (s *Server) Argv(args ...string) []string {
	return append([]string{s.Bin.Path, "-S", s.Socket, "-f", "/dev/null"}, args...)
}

// Env is the environment for a child that talks to this server: the test's
// own without TMUX, with TMUX_TMPDIR set to Dir and TERM set.
func (s *Server) Env() []string { return append([]string(nil), s.env...) }

// Alive reports whether has-session finds the server.
func (s *Server) Alive() bool {
	_, err := s.Try("has-session")
	return err == nil
}

// Kill runs kill-server and fails the test if has-session still finds the
// server afterwards. Killing a server that is already gone is fine.
func (s *Server) Kill(tb testing.TB) {
	tb.Helper()
	_, err := s.Try("kill-server")
	var exit *proc.ExitError
	if err != nil && !errors.As(err, &exit) {
		tb.Errorf("kill-server: %v", err)
	}
	if s.Alive() {
		tb.Errorf("tmux server %s is still alive after kill-server", s.Socket)
	}
}

// shortTempDir makes a new folder whose socket path stays short: the
// system temp folder when it is short enough, else /tmp. t.TempDir is too
// long on macOS for a socket path.
func shortTempDir(prefix string) (string, error) {
	for _, base := range []string{os.TempDir(), "/tmp"} {
		if len(filepath.Join(base, prefix+"0123456789", "s")) > maxSocketPath {
			continue
		}
		dir, err := os.MkdirTemp(base, prefix)
		if err != nil {
			return "", fmt.Errorf("tmuxtest: socket folder: %w", err)
		}
		return dir, nil
	}
	return "", errors.New("tmuxtest: no temp folder short enough for a socket path")
}

// cleanEnv is the process environment without TMUX and TMUX_TMPDIR, plus
// TMUX_TMPDIR=dir and a TERM tmux knows.
func cleanEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_TMPDIR=") || strings.HasPrefix(kv, "TERM=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TMUX_TMPDIR="+dir, "TERM=xterm-256color")
}
