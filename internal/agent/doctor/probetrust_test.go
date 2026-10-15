package doctor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

// trustFixture loads a committed harness trust fixture: the same screens the
// probe classifies as a trust prompt.
func trustFixture(t *testing.T, harness, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "harness", harness, "testdata", file)) //nolint:gosec // G304: fixed testdata names in this repo
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type trustCapture func(context.Context, string, string) (string, bool, error)

func runProbeTrust(t *testing.T, capture trustCapture) doctor.Result {
	t.Helper()
	r := newRig(t)
	opts := r.opts()
	opts.ProbeTrustCapture = capture
	rep := doctor.Run(t.Context(), opts, doctor.Local()...)
	for _, c := range rep.Checks {
		if c.ID == "probe_trust" {
			return c
		}
	}
	t.Fatal("no check probe_trust")
	return doctor.Result{}
}

func TestProbeTrustFailsWithTheOneTimeStepWhenATrustScreenShows(t *testing.T) {
	t.Parallel()
	screens := map[string]string{
		"claude": trustFixture(t, "claude", filepath.Join("2.1.296", "trust-folder.txt")),
		"codex":  trustFixture(t, "codex", filepath.Join("0.160.0", "trust-folder-git.txt")),
		"muse":   trustFixture(t, "muse", filepath.Join("1.4.4", "trust-workspace.txt")),
		"cursor": trustFixture(t, "cursor", filepath.Join("2026.10.01", "trust-workspace.txt")),
	}
	c := runProbeTrust(t, func(_ context.Context, harness, _ string) (string, bool, error) {
		return screens[harness], true, nil
	})
	if c.Status != doctor.Fail {
		t.Fatalf("probe_trust status = %s (%q, fix %q), want fail", c.Status, c.Reason, c.Fix)
	}
	text := c.Reason + " | " + c.Fix
	for _, w := range []string{"Claude Code", "Codex", "Muse", "Cursor", "trust", "/exit"} {
		if !strings.Contains(text, w) {
			t.Errorf("probe_trust output does not mention %q: %q", w, text)
		}
	}
}

func TestProbeTrustIsOkWhenNoTrustScreenShows(t *testing.T) {
	t.Parallel()
	ready := trustFixture(t, "claude", filepath.Join("2.1.296", "ready-ansi.txt"))
	c := runProbeTrust(t, func(_ context.Context, harness, _ string) (string, bool, error) {
		if harness == "claude" {
			return ready, true, nil
		}
		return "", false, nil
	})
	if c.Status != doctor.OK {
		t.Fatalf("probe_trust status = %s (%q, fix %q), want ok", c.Status, c.Reason, c.Fix)
	}
	if c.Fix != "" {
		t.Errorf("an ok check must have no fix line: %q", c.Fix)
	}
}

func TestProbeTrustIsUnknownWhenNothingCouldBeRead(t *testing.T) {
	t.Parallel()
	c := runProbeTrust(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, errors.New("tmux would not answer")
	})
	if c.Status != doctor.Unknown {
		t.Fatalf("probe_trust status = %s (%q, fix %q), want unknown", c.Status, c.Reason, c.Fix)
	}
	if c.Fix == "" {
		t.Error("an unknown check must have a fix line")
	}
}

func TestProbeTrustIsUnknownWithoutACaptureFunc(t *testing.T) {
	t.Parallel()
	c := runProbeTrust(t, nil)
	if c.Status != doctor.Unknown {
		t.Fatalf("probe_trust status = %s (%q, fix %q), want unknown", c.Status, c.Reason, c.Fix)
	}
}
