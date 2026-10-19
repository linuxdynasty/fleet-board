package fleetcli_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/fleetcli"
)

func TestRunRefusesPresentTokenBeforeReadingTheNetwork(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := fleetcli.Run(context.Background(), []string{"status", "--json"}, fleetcli.Input{
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

type shortOutput struct{}

func (shortOutput) Write([]byte) (int, error) { return 0, nil }

var _ io.Writer = shortOutput{}

func TestOutputWriteFailureIsReported(t *testing.T) {
	h := startHub(t)
	var diagnostic bytes.Buffer
	code := fleetcli.Run(t.Context(), []string{"machines", "--json"}, fleetcli.Input{
		Stdout: shortOutput{}, Stderr: &diagnostic,
		LookupEnv: func(name string) (string, bool) {
			if name == "FLEET_URL" {
				return h.url, true
			}
			return "", false
		},
	})
	if code != 1 || !strings.Contains(diagnostic.String(), "write output") {
		t.Fatalf("short output = %d, %q", code, diagnostic.String())
	}
}

func TestInvalidArgumentsStopBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"unknown", []string{"nope"}},
		{"extra", []string{"status", "extra"}},
		{"duplicate flag", []string{"machines", "--page-size", "1", "--page-size", "2"}},
		{"zero page size", []string{"machines", "--page-size", "0"}},
		{"large page size", []string{"machines", "--page-size", "501"}},
		{"list flag on show", []string{"machines", "show", "id", "--filter", "name=foo"}},
		{"empty ID", []string{"machines", "show", ""}},
		{"work no selector", []string{"work", "show", "--kind", "ISSUE", "--project", "", "--key", "1"}},
		{"work two selectors", []string{"work", "show", "--kind", "ISSUE", "--project", "", "--key", "1", "--integration", "i", "--provider", "p"}},
		{"work numeric kind", []string{"work", "show", "--kind", "1", "--project", "", "--key", "1", "--provider", "p"}},
		{"work no project flag", []string{"work", "show", "--kind", "ISSUE", "--key", "1", "--provider", "p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			code := fleetcli.Run(t.Context(), tc.args, fleetcli.Input{
				Stdout: &out, Stderr: &diag,
				LookupEnv: func(name string) (string, bool) {
					if name == "FLEET_URL" {
						return "http://127.0.0.1:1", true
					}
					return "", false
				},
			})
			if code != 2 || out.Len() != 0 {
				t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), diag.String())
			}
		})
	}
}

func TestVersionAndHelpDoNotInspectEnvironment(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}, {"-h"}} {
		var out, diag bytes.Buffer
		code := fleetcli.Run(t.Context(), args, fleetcli.Input{
			Stdout: &out, Stderr: &diag,
			LookupEnv: func(string) (string, bool) { t.Fatal("help inspected environment"); return "", false },
			Version:   "test-version",
		})
		if code != 0 || out.Len() == 0 || diag.Len() != 0 {
			t.Fatalf("%v = %d, %q, %q", args, code, out.String(), diag.String())
		}
	}
}
