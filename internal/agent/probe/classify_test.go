package probe

import (
	"os"
	"strings"
	"testing"
)

func loadScreen(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the table's own fixture paths under the repo
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return string(b)
}

// TestClassify reads the harness teams' own fixtures: trust screens stay
// trust on every version, the Codex update prompt stays a startup screen,
// ready screens stay ready, and anything else (drafts, empty, truncated,
// oversized) stays unknown so the pass types nothing.
func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		harness string
		file    string
		// inline is used instead of file for crafted screens.
		inline  string
		outcome Outcome
		kind    string
	}{
		// Trust: the owner must trust the folder by hand (01 C3).
		{name: "claude trust", harness: "claude", file: "../../harness/claude/testdata/2.1.296/trust-folder.txt", outcome: OutcomeTrust},
		{name: "codex trust 0.160.0", harness: "codex", file: "../../harness/codex/testdata/0.160.0/trust-folder-git.txt", outcome: OutcomeTrust},
		{name: "codex trust 0.162.1", harness: "codex", file: "../../harness/codex/testdata/0.162.1/trust-folder-git.txt", outcome: OutcomeTrust},
		{name: "muse trust 1.4.4", harness: "muse", file: "../../harness/muse/testdata/1.4.4/trust-workspace.txt", outcome: OutcomeTrust},
		{name: "muse trust git root 1.4.4", harness: "muse", file: "../../harness/muse/testdata/1.4.4/trust-workspace-git-root.txt", outcome: OutcomeTrust},
		{name: "muse trust plain folder 1.4.4", harness: "muse", file: "../../harness/muse/testdata/1.4.4/trust-workspace-plain-folder.txt", outcome: OutcomeTrust},
		{name: "muse trust 1.4.6", harness: "muse", file: "../../harness/muse/testdata/1.4.6/trust-workspace.txt", outcome: OutcomeTrust},
		{name: "muse trust 1.4.7", harness: "muse", file: "../../harness/muse/testdata/1.4.7/trust-workspace.txt", outcome: OutcomeTrust},
		{name: "cursor trust", harness: "cursor", file: "../../harness/cursor/testdata/2026.10.01/trust-workspace.txt", outcome: OutcomeTrust},

		// Startup: never answered, failover tried (03b).
		{name: "codex update prompt", harness: "codex", file: "../../harness/codex/testdata/0.160.0/startup-update-prompt.txt", outcome: OutcomeStartup, kind: KindUpdate},
		{name: "codex daemon settings types nothing", harness: "codex", file: "../../harness/codex/testdata/0.160.0/startup-daemon-settings-incompatible.txt", outcome: OutcomeUnknown},
		{name: "claude release notes dialog", harness: "claude", file: "../../harness/claude/testdata/2.1.296/release-notes.txt", outcome: OutcomeStartup, kind: KindReleaseNotes},

		// Ready: the only screens a status command is ever typed into.
		{name: "codex ready 0.160.0", harness: "codex", file: "../../harness/codex/testdata/0.160.0/ready.txt", outcome: OutcomeReady},
		{name: "codex ready 0.162.1", harness: "codex", file: "../../harness/codex/testdata/0.162.1/startup-ready.txt", outcome: OutcomeReady},
		{name: "codex update banner ready", harness: "codex", file: "../../harness/codex/testdata/0.160.0/startup-ready-update-banner.txt", outcome: OutcomeReady},
		{name: "muse ready 1.4.4", harness: "muse", file: "../../harness/muse/testdata/1.4.4/startup-ready.txt", outcome: OutcomeReady},
		{name: "muse ready 1.4.6", harness: "muse", file: "../../harness/muse/testdata/1.4.6/startup-ready.txt", outcome: OutcomeReady},
		{name: "muse ready 1.4.7", harness: "muse", file: "../../harness/muse/testdata/1.4.7/startup-ready.txt", outcome: OutcomeReady},
		{name: "claude ready ansi", harness: "claude", file: "../../harness/claude/testdata/2.1.296/ready-ansi.txt", outcome: OutcomeReady},
		{name: "cursor ready ansi", harness: "cursor", file: "../../harness/cursor/testdata/2026.10.01/ready-ansi.txt", outcome: OutcomeReady},

		// Unknown: drafts, empty, truncated and oversized screens type nothing.
		{name: "codex draft", harness: "codex", file: "../../harness/codex/testdata/0.160.0/ready-input-has-draft.txt", outcome: OutcomeUnknown},
		{name: "codex draft ansi 0.162.1", harness: "codex", file: "../../harness/codex/testdata/0.162.1/ready-input-has-draft-ansi.txt", outcome: OutcomeUnknown},
		{name: "muse draft", harness: "muse", file: "../../harness/muse/testdata/1.4.6/ready-input-has-draft.txt", outcome: OutcomeUnknown},
		{name: "claude draft ansi", harness: "claude", file: "../../harness/claude/testdata/2.1.296/ready-input-has-draft-ansi.txt", outcome: OutcomeUnknown},
		{name: "claude plain placeholder stays unknown", harness: "claude", file: "../../harness/claude/testdata/2.1.296/startup-ready.txt", outcome: OutcomeUnknown},
		{name: "cursor draft", harness: "cursor", file: "../../harness/cursor/testdata/2026.10.01/ready-input-has-draft.txt", outcome: OutcomeUnknown},
		{name: "empty screen", harness: "codex", inline: "", outcome: OutcomeUnknown},
		{name: "unrelated text", harness: "codex", inline: "hello\nworld\n", outcome: OutcomeUnknown},
		{name: "truncated trust heading without answer row", harness: "codex", inline: "Folder access\n~/scratch\n", outcome: OutcomeUnknown},
		{name: "oversized screen", harness: "codex", inline: strings.Repeat("x", 65*1024), outcome: OutcomeUnknown},
		{name: "unknown harness", harness: "opencode", inline: "anything", outcome: OutcomeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			screen := tc.inline
			if tc.file != "" {
				screen = loadScreen(t, tc.file)
			}
			got := Classify(tc.harness, screen)
			if got.Outcome != tc.outcome || got.Kind != tc.kind {
				t.Errorf("Classify(%s) = %+v, want outcome %v kind %q", tc.harness, got, tc.outcome, tc.kind)
			}
		})
	}
}

func TestStatusCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		harness string
		command string
		ok      bool
	}{
		{harness: "claude", command: "/usage", ok: true},
		{harness: "codex", command: "/status", ok: true},
		{harness: "muse", command: "/usage", ok: true},
		{harness: "cursor", command: "/usage", ok: true},
		{harness: "opencode", ok: false},
		{harness: "", ok: false},
	}
	for _, tc := range cases {
		t.Run("harness="+tc.harness, func(t *testing.T) {
			t.Parallel()
			got, ok := StatusCommand(tc.harness)
			if ok != tc.ok || got != tc.command {
				t.Errorf("StatusCommand(%q) = %q, %v; want %q, %v", tc.harness, got, ok, tc.command, tc.ok)
			}
		})
	}
}
