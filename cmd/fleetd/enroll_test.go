package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var tokenLine = regexp.MustCompile(`^fbe1_[A-Za-z0-9_-]+\n$`)

// enrollToken runs `fleetd enroll-token` and returns stdout, stderr and the
// exit code.
func enrollToken(t *testing.T, config string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"enroll-token", "--config", config}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), exitCode(err)
}

func storedCA(t *testing.T, state string) []byte {
	t.Helper()
	s, err := statedb.Open(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, err := statedb.ReadIdentity(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return id.CACert
}

func stopWith(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if sig == syscall.SIGTERM && exitCode(err) != 0 {
			t.Errorf("fleetd run after SIGTERM exit %d, want 0", exitCode(err))
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("fleetd run did not exit within 20 s of %v", sig)
	}
}

// Row 2b's acceptance: a fresh init, then run, then enroll-token prints a
// token whose CA hash equals the stored CA.
func TestEnrollTokenPrintsATokenForTheStoredCA(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	if out, err := exec.Command(bin, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	caDER := storedCA(t, state)
	fp := sha256.Sum256(caDER)
	hub, _ := startRun(t, config)
	sock := filepath.Join(state, "admin.sock")

	stdout, stderr, code := enrollToken(t, config, "--machine", "laptop", "--label", "gpu", "--label", "home",
		"--expect-host", "laptop.lan", "--start-drained")
	if code != 0 {
		t.Fatalf("enroll-token exit %d\n%s", code, stderr)
	}
	if !tokenLine.MatchString(stdout) {
		t.Fatalf("stdout %q is not one fbe1_ token line", stdout)
	}
	tok := strings.TrimSuffix(stdout, "\n")
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, "fbe1_"))
	if err != nil || len(raw) != 65 || raw[0] != 1 {
		t.Fatalf("token decodes to %d bytes (err %v), want 65 starting with 1", len(raw), err)
	}
	if !bytes.Equal(raw[1:33], fp[:]) {
		t.Error("the token's CA hash is not SHA-256 of the stored CA certificate")
	}
	for _, want := range []string{"expires", hex.EncodeToString(fp[:]), "fleet-agent enroll --token " + tok} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not hold %q:\n%s", want, stderr)
		}
	}

	again, _, code := enrollToken(t, config, "--machine", "laptop")
	if code != 0 || !tokenLine.MatchString(again) || again == stdout {
		t.Errorf("second enroll-token: exit %d, stdout %q; want a different token", code, again)
	}

	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"--ttl 25h", []string{"--machine", "laptop", "--ttl", "25h"}, 1},
		{"--ttl 0", []string{"--machine", "laptop", "--ttl", "0"}, 1},
		{"no --machine", nil, 2},
		{"bad label", []string{"--machine", "laptop", "--label", "Bad Label"}, 1},
	} {
		out, errOut, code := enrollToken(t, config, tc.args...)
		if code != tc.code || out != "" {
			t.Errorf("%s: exit %d, stdout %q; want exit %d and no stdout\n%s", tc.name, code, out, tc.code, errOut)
		}
	}

	stopWith(t, hub, syscall.SIGTERM)
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("admin.sock is still there after SIGTERM: %v", err)
	}
}

func TestEnrollTokenWithNoHubOrAKilledHub(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	if out, err := exec.Command(bin, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	notRunning := func(when string) {
		t.Helper()
		before := listing(t, state)
		stdout, stderr, code := enrollToken(t, config, "--machine", "laptop")
		if code != 1 || stdout != "" || !strings.Contains(stderr, "fleetd is not running") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want 1, nothing, \"fleetd is not running\"", when, code, stdout, stderr)
		}
		if after := listing(t, state); after != before {
			t.Errorf("%s: the state folder changed:\n%s\n%s", when, before, after)
		}
	}
	notRunning("never started")

	killed, _ := startRun(t, config)
	stopWith(t, killed, syscall.SIGKILL)
	if _, err := os.Lstat(filepath.Join(state, "admin.sock")); err != nil {
		t.Fatalf("admin.sock is gone after SIGKILL (%v); the next checks need it left behind", err)
	}
	notRunning("after SIGKILL")

	next, _ := startRun(t, config)
	if stdout, stderr, code := enrollToken(t, config, "--machine", "laptop"); code != 0 || !tokenLine.MatchString(stdout) {
		t.Errorf("enroll-token after a restart past a leftover socket: exit %d, stdout %q\n%s", code, stdout, stderr)
	}
	stopWith(t, next, syscall.SIGTERM)
}

// listing names every entry in dir with its size and mode.
func listing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(e.Name() + " " + fi.Mode().String() + " " + fi.ModTime().String())
		if fi.Mode().IsRegular() {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			b.WriteString(" " + hex.EncodeToString(sum[:]))
		}
		b.WriteString("\n")
	}
	return b.String()
}
