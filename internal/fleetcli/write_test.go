package fleetcli_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/linuxdynasty/fleet-board/internal/fleetcli"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedGitHubRow gives the hub a code-host integration before it starts, so
// CLI tests can watch repos through CreateRepo without touching SQL.
func seedGitHubRow(t *testing.T, state string) {
	t.Helper()
	st, err := statedb.Open(t.Context(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC).UnixMilli()
	if err := statedb.EnsureIntegration(t.Context(), st, statedb.IntegrationRow{
		ID: "github", Provider: "github", BaseURL: "https://api.github.com",
		Label: "github", Spec: []byte("{}"), Status: []byte("{}"),
	}, now); err != nil {
		t.Fatal(err)
	}
}

// invokeSecret runs the CLI on a terminal, answers the approval-secret
// prompt, and reports the exit code with both streams. When the command
// stops before prompting (bad usage), it returns without writing anything.
func invokeSecret(t *testing.T, h localHub, typed string, args ...string) (int, string, string) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	prompt := &watchedPrompt{ready: make(chan struct{}, 1)}
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- fleetcli.Run(t.Context(), args, fleetcli.Input{
			Stdin: slave, Stdout: &out, Stderr: prompt,
			LookupEnv: func(name string) (string, bool) {
				if name == "FLEET_URL" {
					return h.url, true
				}
				return "", false
			},
		})
	}()
	select {
	case <-prompt.ready:
		if _, err := master.Write([]byte(typed + "\n")); err != nil {
			t.Fatal(err)
		}
	case code := <-done:
		return code, out.String(), prompt.String()
	case <-time.After(4 * time.Second):
		t.Fatal("command neither prompted nor finished")
	}
	select {
	case code := <-done:
		if strings.Contains(prompt.String(), typed) {
			t.Error("terminal input leaked to stderr")
		}
		return code, out.String(), prompt.String()
	case <-time.After(10 * time.Second):
		t.Fatal("command did not finish after the secret")
		return 0, "", ""
	}
}

// invokeWithToken runs the CLI with FLEET_TOKEN set, proving the refusal
// happens before any network read.
func invokeWithToken(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	code := fleetcli.Run(t.Context(), args, fleetcli.Input{
		Stdout: &out, Stderr: &diagnostic,
		LookupEnv: func(name string) (string, bool) {
			if name == "FLEET_TOKEN" {
				return "sensitive-token", true
			}
			return "", false
		},
	})
	return code, out.String(), diagnostic.String()
}

func TestWriteRefusesAPresentToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := fleetcli.Run(t.Context(), []string{"repo", "create", "github.com/acme/api"}, fleetcli.Input{
		Stdout: &stdout, Stderr: &stderr,
		LookupEnv: func(name string) (string, bool) {
			if name == "FLEET_TOKEN" {
				return "sensitive-token", true
			}
			return "", false
		},
	})
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "API tokens") {
		t.Fatalf("Run = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "sensitive-token") {
		t.Fatal("diagnostic printed token")
	}
}

func TestWriteNeedsATerminalForItsSecret(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invoke(t, h, "repo", "create", "github.com/acme/api")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "terminal") {
		t.Fatalf("nonterminal write = %d, %q, %q", code, out, diagnostic)
	}
}
