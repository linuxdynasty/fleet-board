package fleetcli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/seed"
)

// createdAccountID reads the random account id out of a create response.
func createdAccountID(t *testing.T, createJSON string) string {
	t.Helper()
	var res struct {
		Account struct {
			ID struct {
				Value string `json:"value"`
			} `json:"id"`
		} `json:"account"`
	}
	if err := json.Unmarshal([]byte(createJSON), &res); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if res.Account.ID.Value == "" {
		t.Fatalf("create response has no account id: %s", createJSON)
	}
	return res.Account.ID.Value
}

func TestAccountCreateSetsDefaults(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "opencode", "--provider", "anthropic",
		"--display-name", "work", "--request-id", "cli-acc-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"reservePercent":10`) {
		t.Fatalf("default reserve = %s", out)
	}
	if !strings.Contains(out, `"CONSENT_ACT_PUSH"`) || !strings.Contains(out, `"CONSENT_ACT_PR_OPEN"`) || !strings.Contains(out, `"CONSENT_ACT_MERGE"`) {
		t.Fatalf("default consent = %s", out)
	}
	// The account id is random per hub; the golden pins the shape, not it.
	golden(t, "account-create", strings.ReplaceAll(out, createdAccountID(t, out), "<account-id>"))
}

func TestAccountUpdateSetsReserve(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, created, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "opencode", "--provider", "anthropic",
		"--display-name", "work", "--request-id", "cli-acc-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	id := createdAccountID(t, created)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"account", "update", id,
		"--reserve-percent", "25", "--expected-version", "1",
		"--request-id", "cli-acc-2", "--json")
	if code != 0 {
		t.Fatalf("update = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"reservePercent":25`) || !strings.Contains(out, `"version":"2"`) {
		t.Fatalf("update output = %s", out)
	}
	golden(t, "account-update", strings.ReplaceAll(out, id, "<account-id>"))
}

func TestAccountUpdateMapsBindings(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	code, created, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "codex", "--provider", "openai",
		"--display-name", "codex-main", "--request-id", "cli-acc-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	id := createdAccountID(t, created)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"account", "update", id,
		"--spec-json", `{"bindings":[{"machine":{"value":"`+seed.MachineLaptop+`"},"profile":""}]}`,
		"--update-mask", "spec.bindings",
		"--request-id", "cli-acc-2", "--json")
	if code != 0 {
		t.Fatalf("bindings update = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, seed.MachineLaptop) {
		t.Fatalf("bindings output = %s", out)
	}
}

func TestAccountDeleteForgetsAnAccount(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, created, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "opencode", "--provider", "anthropic",
		"--display-name", "work", "--request-id", "cli-acc-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	id := createdAccountID(t, created)
	code, out, _ := invokeSecret(t, h, h.secret,
		"account", "delete", id, "--request-id", "cli-acc-2")
	if code != 2 || out != "" {
		t.Fatalf("delete without --yes = %d, %q", code, out)
	}
	code, out, diagnostic = invokeSecret(t, h, h.secret,
		"account", "delete", id,
		"--expected-version", "1", "--request-id", "cli-acc-2", "--yes")
	if code != 0 || !strings.Contains(out, id) {
		t.Fatalf("delete = %d, %q, %q", code, out, diagnostic)
	}
}

func TestAccountStaleVersionIsRefused(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, created, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "opencode", "--provider", "anthropic",
		"--display-name", "work", "--request-id", "cli-acc-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	id := createdAccountID(t, created)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"account", "update", id,
		"--reserve-percent", "25", "--expected-version", "99", "--request-id", "cli-acc-2")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("stale = %d, %q, %q", code, out, diagnostic)
	}
}

func TestAccountUsageErrors(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"create without harness", []string{"account", "create", "--provider", "anthropic", "--display-name", "w"}},
		{"create without provider", []string{"account", "create", "--harness", "opencode", "--display-name", "w"}},
		{"create without name", []string{"account", "create", "--harness", "opencode", "--provider", "anthropic"}},
		{"create bad mode", []string{"account", "create", "--harness", "opencode", "--provider", "anthropic", "--display-name", "w", "--mode", "nope"}},
		{"update without changes", []string{"account", "update", "work", "--request-id", "x"}},
		{"update bad consent", []string{"account", "update", "work", "--consent", "DELETE", "--request-id", "x"}},
		{"delete without id", []string{"account", "delete", "--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := invokeSecret(t, h, h.secret, tc.args...)
			if code != 2 || out != "" {
				t.Fatalf("%v = %d, %q", tc.args, code, out)
			}
		})
	}
}

// TestAccountCreateKeepsAnExplicitZeroReserve proves presence survives the
// create path: --reserve-percent 0 stores a zero reserve instead of
// silently defaulting to 10.
func TestAccountCreateKeepsAnExplicitZeroReserve(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "opencode", "--provider", "anthropic",
		"--display-name", "flat", "--reserve-percent", "0",
		"--request-id", "cli-acc-zero", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"reservePercent":0`) {
		t.Fatalf("explicit zero reserve = %s, want reservePercent 0", out)
	}
}
