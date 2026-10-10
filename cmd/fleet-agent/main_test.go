package main_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fleet-agent-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin = filepath.Join(dir, "fleet-agent")
	// Tests never send real multicast: the binary's hub finder finds
	// nothing, unless FLEET_MDNS_LIVE asks for the real one.
	tags := "-tags=fleet_nomdns"
	if os.Getenv("FLEET_MDNS_LIVE") != "" {
		tags = "-tags="
	}
	build := exec.CommandContext(context.Background(), "go", "build", tags, "-o", bin, ".") //nolint:gosec // G204: bin is a fresh temp path; every other argument is a literal
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	code           int
	stdout, stderr string
}

// agent runs the built binary with args and an empty HOME.
func agent(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, args...) //nolint:gosec // G204: bin is the binary TestMain built; args come from the tests
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return result{code, out.String(), errOut.String()}
}

func TestVersionAndUsage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		args   []string
		code   int
		stdout string
	}{
		{"version", []string{"--version"}, 0, "fleet-agent dev\n"},
		{"no arguments prints help", nil, 0, "fleet-agent is the fleet-board agent"},
		{"unknown command", []string{"frobnicate"}, 2, ""},
		{"enroll without a token", []string{"enroll", "--hub", "127.0.0.1"}, 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := agent(t, tc.args...)
			if r.code != tc.code || !strings.Contains(r.stdout, tc.stdout) {
				t.Errorf("fleet-agent %v = %d %q %q; want %d and stdout with %q", tc.args, r.code, r.stdout, r.stderr, tc.code, tc.stdout)
			}
		})
	}
}

// A token typed without --token is bad usage, and is never echoed back.
func TestAStrayArgumentIsNeverEchoed(t *testing.T) {
	t.Parallel()
	const secret = "fbe1_SECRETSECRETSECRET" //nolint:gosec // G101: a fake token the test checks is never echoed
	for _, args := range [][]string{{secret}, {"enroll", secret}, {"enroll", "--hub", "127.0.0.1", secret}} {
		r := agent(t, args...)
		if r.code != 2 || strings.Contains(r.stdout+r.stderr, "SECRETSECRET") {
			t.Errorf("fleet-agent %d args = %d, output %q; want 2 and no echo", len(args), r.code, r.stdout+r.stderr)
		}
	}
}

// The hub's expected hostname is checked: a mismatch prints one warning on
// stderr, and a match prints none.
func TestEnrollWarnsOnAHostnameMismatch(t *testing.T) {
	t.Parallel()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	h := conntest.StartHub(t, conntest.Options{})
	cases := []struct {
		expect string
		warn   bool
	}{{"not-this-machine", true}, {host, false}}
	for _, tc := range cases {
		dir := filepath.Join(t.TempDir(), "state")
		r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.TokenExpecting(t, tc.expect, time.Now().Add(time.Hour)))
		if r.code != 0 {
			t.Fatalf("enroll = %d: %s", r.code, r.stderr)
		}
		warned := strings.Count(r.stderr, "fleet-agent: warning:")
		// The hub compares with the machine name when the owner gave no
		// hostname, so the warning names the hub, not the token.
		named := strings.Contains(r.stderr, "the hub expected hostname "+strconv.Quote(tc.expect))
		if (warned == 1) != tc.warn || warned > 1 || (tc.warn && !named) {
			t.Errorf("expecting %q: stderr %q; want a warning %v", tc.expect, r.stderr, tc.warn)
		}
	}
}

func TestEnrollCommand(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir := filepath.Join(t.TempDir(), "state")
	tok := h.Token(t, time.Now().Add(time.Hour))

	r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", tok)
	if r.code != 0 {
		t.Fatalf("enroll = %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	id, err := conn.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.stdout, id.MachineID) || !strings.Contains(r.stdout, `"laptop"`) || !strings.Contains(r.stdout, h.FleetID) {
		t.Errorf("stdout %q does not name the machine, its name and the fleet", r.stdout)
	}
	if strings.Contains(r.stdout+r.stderr, tok[5:]) {
		t.Error("the output holds the token")
	}

	again := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(time.Hour)))
	if again.code != 1 || !strings.Contains(again.stderr, "already enrolled") || !strings.Contains(again.stderr, id.MachineID) {
		t.Errorf("enroll on an enrolled machine = %d %q, want 1 naming %s", again.code, again.stderr, id.MachineID)
	}
}

// Every refusal exits 1 with a line that says why, and the hub is never
// reached when the agent can tell first.
func TestEnrollFailures(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	cases := []struct {
		name    string
		args    func(t *testing.T, dir string) []string
		want    string
		reaches bool
	}{
		{"no hub address", func(t *testing.T, dir string) []string {
			t.Helper()
			return []string{"--state-dir", dir, "enroll", "--token", h.Token(t, time.Now().Add(time.Hour))}
		}, "no hub found on the local network", false},
		{"malformed token", func(_ *testing.T, dir string) []string {
			return []string{"--state-dir", dir, "enroll", "--hub", h.Addr, "--token", "fbe1_short"}
		}, "not a fleet-board token", false},
		{"expired token", func(t *testing.T, dir string) []string {
			t.Helper()
			return []string{"--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(-time.Minute))}
		}, "ENROLL_TOKEN_EXPIRED", true},
		{"token for another hub's CA", func(t *testing.T, dir string) []string {
			t.Helper()
			return []string{"--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.TokenForCA(t, []byte("another CA"), time.Now().Add(time.Hour))}
		}, "does not match the CA in the token", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			before := h.Requests()
			r := agent(t, tc.args(t, dir)...)
			if r.code != 1 || !strings.Contains(r.stderr, tc.want) {
				t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, tc.want)
			}
			if reached := h.Requests() > before; reached != tc.reaches {
				t.Errorf("hub reached = %v, want %v", reached, tc.reaches)
			}
			if _, err := conn.LoadIdentity(dir); !errors.Is(err, conn.ErrNotEnrolled) {
				t.Errorf("LoadIdentity = %v, want ErrNotEnrolled", err)
			}
		})
	}
}
