package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// probeTrustBinaries mirrors the probe's harness-to-program map (the probe
// keeps it private): the program doctor starts in the probe folder for one
// trust look. OpenCode has no trust screen and takes no session.
var probeTrustBinaries = map[string]string{
	"claude": "claude",
	"codex":  "codex",
	"muse":   "muse",
	"cursor": "cursor-agent",
}

var (
	errProbeNoTmux          = errors.New("tmux is not installed")
	errProbeScreenUnsettled = errors.New("the harness screen never settled")
	errProbeNoDir           = errors.New("the probe folder does not exist")
)

// probeTrustSettle bounds the wait for a stable first screen: twelve reads
// 250 ms apart, about 3 seconds. A screen still changing then is unsettled,
// and the check says so instead of guessing.
const (
	probeTrustSettlePolls = 12
	probeTrustSettleEvery = 250 * time.Millisecond
)

// probeTrustCapture is doctor's ProbeTrustCapture (01 C3): it starts one
// harness in probeDir on a throwaway tmux server, reads its first settled
// screen, sends no key and tears the session and the server down. It never
// touches the developer's own server or the fleet-probe server: the socket
// lives in a temp folder under an explicit -S path. A harness that is not
// installed is a skip (installed=false), never an error.
func probeTrustCapture(ctx context.Context, harness, probeDir string) (string, bool, error) {
	binary, ok := probeTrustBinaries[harness]
	if !ok {
		return "", false, nil
	}
	// A folder that is not there cannot show a trust screen, and doctor
	// creates nothing: the probe-folder check already tells the owner to
	// make it. This also keeps an empty machine side-effect free.
	if st, err := os.Stat(probeDir); err != nil || !st.IsDir() {
		return "", false, errProbeNoDir
	}
	path, ok := lookPath(binary)
	if !ok {
		return "", false, nil
	}
	tmux, err := probeTrustTmux()
	if err != nil {
		return "", false, err
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		return "", false, err
	}
	tmp, err := os.MkdirTemp("", "fleet-doctor-probe-")
	if err != nil {
		return "", false, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	socket := filepath.Join(tmp, "s")
	be, err := probe.NewTMux(runner, tmux, socket)
	if err != nil {
		return "", false, err
	}
	name := "doctor-trust-" + harness
	if err := be.NewSession(ctx, name, probeDir, path); err != nil {
		return "", false, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = be.KillSession(cctx, name)
		_ = probeTrustKillServer(cctx, runner, tmux, socket)
	}()
	screen, settled := probeTrustSettle(ctx, be, name)
	if !settled {
		return "", false, errProbeScreenUnsettled
	}
	return screen, true, nil
}

// probeTrustTmux finds tmux the way the machine facts do: PATH first, then
// the system folders.
func probeTrustTmux() (string, error) {
	if p, ok := lookPath("tmux"); ok {
		return p, nil
	}
	for _, dir := range machine.DefaultTmuxDirs {
		p := filepath.Join(dir, "tmux")
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", errProbeNoTmux
}

// lookPath finds name on PATH without starting anything (depguard keeps
// os/exec in internal/proc, so this is a stat loop, not a lookup call).
func lookPath(name string) (string, bool) {
	if strings.ContainsRune(name, '/') {
		if isExecutable(name) {
			return name, true
		}
		return "", false
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, true
		}
	}
	return "", false
}

func isExecutable(path string) bool {
	//nolint:gosec // G703: path is a fixed program name joined to the user's own PATH entries, never remote input
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// probeTrustSettle captures until two back-to-back screens match: only a
// stable screen is classified.
func probeTrustSettle(ctx context.Context, be probe.Backend, name string) (string, bool) {
	prev, err := be.Capture(ctx, name)
	if err != nil {
		return "", false
	}
	for range probeTrustSettlePolls {
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(probeTrustSettleEvery):
		}
		cur, err := be.Capture(ctx, name)
		if err != nil {
			return "", false
		}
		if cur == prev {
			return cur, true
		}
		prev = cur
	}
	return "", false
}

// probeTrustKillServer ends the throwaway server. probe.Backend has no
// kill-server, so this is the one raw tmux call, with separate arguments
// and never a shell string.
func probeTrustKillServer(ctx context.Context, runner *proc.Runner, tmux, socket string) error {
	caller, err := proc.For("tmux")
	if err != nil {
		return err
	}
	_, err = runner.Run(ctx, proc.Spec{Caller: caller, Path: tmux, Args: []string{"-S", socket, "kill-server"}})
	return err
}
