package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// socketPrefix guards every tmux call: the probe only ever talks to a
// throwaway server whose -L name starts with this, never the owner's servers.
const socketPrefix = "fleetprobe-"

// server is one throwaway tmux server reached with -L <socket>.
type server struct {
	bin    string
	socket string
}

func newServer(bin, socket string) (*server, error) {
	if !strings.HasPrefix(socket, socketPrefix) || strings.ContainsAny(socket, "/ ") {
		return nil, fmt.Errorf("socket %q must start with %q and hold no slash or space", socket, socketPrefix)
	}
	return &server{bin: bin, socket: socket}, nil
}

// cleanEnv is the environment for every tmux process: no TMUX (the probe may
// run inside the owner's tmux), no TMUX_TMPDIR, a fixed TERM.
func cleanEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + os.Getenv("HOME"),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"SHELL=/bin/sh",
	}
}

// args prefixes the socket and an empty config file so no user config loads.
func (s *server) args(a ...string) []string {
	return append([]string{"-L", s.socket, "-f", "/dev/null"}, a...)
}

func (s *server) command(ctx context.Context, a ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.bin, s.args(a...)...)
	cmd.Env = cleanEnv()
	return cmd
}

// run runs one plain (non-control) tmux command and returns stdout, stderr
// and the exit code.
func (s *server) run(a ...string) (string, string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := s.command(ctx, a...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		errb.WriteString(err.Error())
	}
	return strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()), code
}

// start creates the server with one session per name, each running cat.
// "exec cat" makes cat the pane process on every system: Ubuntu's dash keeps
// "sh -c cat" as sh, so pane_current_command would read "sh".
func (s *server) start(cols, rows int, sessions ...string) error {
	for _, name := range sessions {
		_, errs, code := s.run("new-session", "-d", "-s", name,
			"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), "exec cat")
		if code != 0 {
			return fmt.Errorf("new-session %s: rc=%d %s", name, code, errs)
		}
	}
	return nil
}

// kill stops this throwaway server and reports whether it is gone.
func (s *server) kill() (killRC int, gone bool) {
	_, _, killRC = s.run("kill-server")
	_, _, hasRC := s.run("has-session")
	return killRC, hasRC != 0
}

func (s *server) version() string {
	out, _, _ := s.run("-V")
	return out
}

// quote wraps a control-mode command argument in single quotes; tmux's
// parser treats # { } ; and spaces specially outside quotes.
func quote(a string) string {
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}
