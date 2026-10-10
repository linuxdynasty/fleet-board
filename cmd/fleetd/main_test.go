package main_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fleetd-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin = filepath.Join(dir, "fleetd")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", bin, ".") //nolint:gosec // G204: bin is a fresh temp path; every other argument is a literal
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fleetd is a command for the binary TestMain built.
func fleetd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	return exec.CommandContext(t.Context(), bin, args...) //nolint:gosec // G204: bin is the binary TestMain built; args come from the tests
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestVersionAndUsage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args       []string
		code       int
		wantStdout string
	}{
		{[]string{"--version"}, 0, "fleetd dev\n"},
		{[]string{"bogus"}, 2, ""},
		{[]string{"run", "--bogus"}, 2, ""},
		{[]string{"run", "extra"}, 2, ""},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			out, err := fleetd(t, tc.args...).Output()
			if got := exitCode(err); got != tc.code {
				t.Errorf("exit %d, want %d", got, tc.code)
			}
			if tc.wantStdout != "" && string(out) != tc.wantStdout {
				t.Errorf("stdout %q, want %q", out, tc.wantStdout)
			}
		})
	}
}

// setup writes a fleetd.yaml with a short state_dir and OS-chosen ports.
func setup(t *testing.T) (config, state string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fbc") //nolint:usetesting // t.TempDir on macOS is too long for the admin socket path config checks
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	config, state = filepath.Join(root, "fleetd.yaml"), filepath.Join(root, "state")
	body := "state_dir: " + state + "\nlisten: {agent: \"127.0.0.1:0\", api: \"127.0.0.1:0\"}\n" +
		"discovery: {mdns: false}\n" // these hubs must never answer mDNS queries on the owner's network
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, state
}

// startRun starts `fleetd run` and returns it with the API port it logged,
// once it logs hub.started: the API listener opens before admin.sock does.
func startRun(t *testing.T, config string) (*exec.Cmd, string) {
	t.Helper()
	cmd, p := startRunPorts(t, config)
	return cmd, p.api
}

// ports are the listener ports `fleetd run` logged.
type ports struct{ api, agent string }

// startRunPorts is startRun returning the agent port too.
func startRunPorts(t *testing.T, config string) (*exec.Cmd, ports) {
	t.Helper()
	cmd := fleetd(t, "run", "--config", config)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	opened := make(chan ports, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		var p ports
		for sc.Scan() {
			var line struct {
				Msg      string  `json:"msg"`
				Listener string  `json:"listener"`
				Port     float64 `json:"port"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil {
				continue
			}
			switch {
			case line.Msg == "hub.listener_opened" && line.Listener == "api":
				p.api = fmt.Sprint(int(line.Port))
			case line.Msg == "hub.listener_opened" && line.Listener == "agent":
				p.agent = fmt.Sprint(int(line.Port))
			case line.Msg == "hub.started" && p.api != "":
				opened <- p
			}
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	select {
	case p := <-opened:
		return cmd, p
	case <-time.After(20 * time.Second):
		t.Fatal("fleetd run did not log hub.started within 20 s")
		return nil, ports{}
	}
}

func TestInitRunSecondRunAndSIGTERM(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	if out, err := fleetd(t, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	if err := fleetd(t, "init", "--config", config).Run(); exitCode(err) != 1 {
		t.Errorf("second fleetd init exit %d, want 1", exitCode(err))
	}

	first, apiPort := startRun(t, config)
	metrics := func() int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+apiPort+"/metrics", nil)
		req.Host = "127.0.0.1:" + apiPort
		resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := metrics(); got != 200 {
		t.Fatalf("/metrics status %d, want 200", got)
	}

	second, err := fleetd(t, "run", "--config", config).CombinedOutput()
	if exitCode(err) != 1 {
		t.Errorf("second fleetd run exit %d, want 1", exitCode(err))
	}
	if !strings.Contains(string(second), "lock_held") {
		t.Errorf("second run output does not name lock_held:\n%s", second)
	}
	if got := metrics(); got != 200 {
		t.Errorf("the first hub stopped serving after the second run: status %d", got)
	}

	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- first.Wait() }()
	select {
	case err := <-done:
		if exitCode(err) != 0 {
			t.Errorf("fleetd run after SIGTERM exit %d, want 0", exitCode(err))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("fleetd run did not exit within 20 s of SIGTERM")
	}

	// A hub killed with SIGKILL leaves fleetd.lock behind; the next run starts.
	killed, _ := startRun(t, config)
	if err := killed.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = killed.Wait()
	if _, err := os.Stat(filepath.Join(state, "fleetd.lock")); err != nil {
		t.Fatalf("fleetd.lock is gone after SIGKILL (%v); the next check needs it left behind", err)
	}
	next, _ := startRun(t, config)
	if err := next.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := next.Wait(); exitCode(err) != 0 {
		t.Errorf("restart after SIGKILL: exit %d, want 0", exitCode(err))
	}
}
