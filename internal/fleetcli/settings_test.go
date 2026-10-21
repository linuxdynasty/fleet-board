package fleetcli_test

import (
	"strings"
	"testing"
)

func TestSettingsUpdateWritesTheFleetLayer(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"settings", "update", "--max-active", "2", "--auto-dispatch",
		"--request-id", "cli-set-1", "--json")
	if code != 0 {
		t.Fatalf("update = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"maxActive":2`) || !strings.Contains(out, `"autoDispatch":true`) {
		t.Fatalf("update output = %s", out)
	}
	golden(t, "settings-update", out)
}

func TestSettingsUpdateDurations(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"settings", "update", "--triage-expiry", "48h", "--probe-interval", "10m",
		"--request-id", "cli-set-1", "--json")
	if code != 0 {
		t.Fatalf("durations update = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"triageExpiry":"172800s"`) || !strings.Contains(out, `"probeInterval":"600s"`) {
		t.Fatalf("durations output = %s", out)
	}
}

func TestSettingsStaleVersionIsRefused(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	if code, _, diagnostic := invokeSecret(t, h, h.secret,
		"settings", "update", "--max-active", "2", "--request-id", "cli-set-1"); code != 0 {
		t.Fatalf("update = %d: %s", code, diagnostic)
	}
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"settings", "update", "--max-active", "3", "--expected-version", "99",
		"--request-id", "cli-set-2")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("stale = %d, %q, %q", code, out, diagnostic)
	}
}

func TestSettingsUsageErrors(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no changes", []string{"settings", "update", "--request-id", "x"}},
		{"bad duration", []string{"settings", "update", "--triage-expiry", "soon"}},
		{"bad server mode", []string{"settings", "update", "--default-server", "TURBO"}},
		{"bad consent", []string{"settings", "update", "--default-consent", "DELETE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := invokeSecret(t, h, h.secret, tc.args...)
			if code != 2 || out != "" {
				t.Fatalf("%v = %d, %q", tc.args, code, out)
			}
		})
	}
}

func TestIntegrationUpdateRelabels(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"settings", "integration", "update", "--id", "github", "--label", "GitHub",
		"--expected-version", "1", "--request-id", "cli-int-1", "--json")
	if code != 0 {
		t.Fatalf("integration update = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"label":"GitHub"`) || !strings.Contains(out, `"version":"2"`) {
		t.Fatalf("integration output = %s", out)
	}
	golden(t, "integration-update", out)
}

func TestIntegrationStaleVersionIsRefused(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"settings", "integration", "update", "--id", "github", "--label", "GitHub",
		"--expected-version", "99", "--request-id", "cli-int-1")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("stale = %d, %q, %q", code, out, diagnostic)
	}
}

func TestIntegrationUsageErrors(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no id", []string{"settings", "integration", "update", "--label", "x"}},
		{"no changes", []string{"settings", "integration", "update", "--id", "github", "--request-id", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := invokeSecret(t, h, h.secret, tc.args...)
			if code != 2 || out != "" {
				t.Fatalf("%v = %d, %q", tc.args, code, out)
			}
		})
	}
}
