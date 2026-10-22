package fleetcli_test

import (
	"strings"
	"testing"
)

func TestRepoCreateWatchesARepo(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-repo-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"github.com/acme/api"`) {
		t.Fatalf("create output = %s", out)
	}
	golden(t, "repo-create", out)
}

func TestRepoUpdateChangesMaskedFields(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	if code, _, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-repo-1"); code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "update", "github.com/acme/api",
		"--enabled=false", "--branch-pattern", "fleet/{work}",
		"--expected-version", "1", "--request-id", "cli-repo-2", "--json")
	if code != 0 {
		t.Fatalf("update = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"version":"2"`) {
		t.Fatalf("update output = %s", out)
	}
	golden(t, "repo-update", out)
}

func TestRepoDeleteForgetsARepo(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	if code, _, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-repo-1"); code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "delete", "github.com/acme/api",
		"--expected-version", "1", "--request-id", "cli-repo-2", "--yes")
	if code != 0 || !strings.Contains(out, "github.com/acme/api") {
		t.Fatalf("delete = %d, %q, %q", code, out, diagnostic)
	}
}

func TestRepoDeleteNeedsYes(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, _ := invokeSecret(t, h, h.secret,
		"repo", "delete", "github.com/acme/api", "--request-id", "cli-repo-1")
	if code != 2 || out != "" {
		t.Fatalf("delete without --yes = %d, %q", code, out)
	}
}

func TestRepoStaleVersionIsRefused(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	if code, _, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-repo-1"); code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "update", "github.com/acme/api",
		"--enabled=false", "--expected-version", "99", "--request-id", "cli-repo-2")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("stale = %d, %q, %q", code, out, diagnostic)
	}
}

func TestRepoUsageErrors(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing id", []string{"repo", "create"}},
		{"bare host", []string{"repo", "create", "github.com"}},
		{"spaced request id", []string{"repo", "create", "github.com/acme/api", "--request-id", "has space"}},
		{"non-ascii request id", []string{"repo", "create", "github.com/acme/api", "--request-id", "caf\u00e9"}},
		{"update without changes", []string{"repo", "update", "github.com/acme/api", "--request-id", "x"}},
		{"bad mode", []string{"repo", "update", "github.com/acme/api", "--worktree-mode", "nope", "--request-id", "x"}},
		{"delete without id", []string{"repo", "delete", "--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := invokeSecret(t, h, h.secret, tc.args...)
			if code != 2 || out != "" {
				t.Fatalf("%v = %d, %q", tc.args, code, out)
			}
		})
	}
}

// TestRepoCreateSpecJSONWithoutEnabledKeepsTheTrueDefault proves a spec
// document that sets other fields but omits enabled still watches an
// enabled repo: only an explicit --enabled or an explicit JSON enabled
// may start a repo paused.
func TestRepoCreateSpecJSONWithoutEnabledKeepsTheTrueDefault(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-repo-spec-1",
		"--spec-json", `{"branch_pattern":"fleet/{work}"}`, "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("spec-json create without enabled = %s, want enabled true", out)
	}
}

// TestRepoCreateSpecJSONExplicitEnabledFalseStaysPaused proves the default
// above does not bulldoze an explicit false: a paused repo stays pausable
// through the document. Proto JSON drops a false scalar, so the test
// asserts the true default was not forced on.
func TestRepoCreateSpecJSONExplicitEnabledFalseStaysPaused(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-repo-spec-2",
		"--spec-json", `{"enabled":false,"branch_pattern":"fleet/{work}"}`, "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	if strings.Contains(out, `"enabled":true`) {
		t.Fatalf("explicit paused create = %s, want no forced enabled true", out)
	}
}
