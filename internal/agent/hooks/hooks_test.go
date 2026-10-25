package hooks_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/hooks"
)

// Seams under test: Definition (the one shared hook definition), rendering
// (RenderFresh), the fleet-entry match rule (IsFleetCommand), the fixed
// command strings (Command) and trust (Trust). All behavior of slice 1.

// TestRenderFreshMatchesGolden renders a fresh user-level config (or Muse
// plugin file) for each harness from the one shared definition and compares
// it with the hand-written golden in testdata (G1).
func TestRenderFreshMatchesGolden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		harness   string
		script    string
		golden    string
		hasScript bool
	}{
		{"claude settings", "claude", "/data/hooks/claude/fleet-goal-hook.py", "testdata/claude/settings.json", true},
		{"codex hooks", "codex", "/data/hooks/codex/fleet-goal-hook.py", "testdata/codex/hooks.json", true},
		{"muse plugin hooks", "muse", "/data/hooks/muse/fleet-goal-hook.py", "testdata/muse/hooks.json", true},
		{"muse plugin manifest", "muse-manifest", "", "testdata/muse/plugin.json", false},
		{"cursor hooks", "cursor", "/data/hooks/cursor/fleet-goal-hook.py", "testdata/cursor/hooks.json", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := hooks.RenderFresh(tc.harness, tc.script)
			if err != nil {
				t.Fatalf("RenderFresh: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(".", tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var gotV, wantV any
			if err := json.Unmarshal(got, &gotV); err != nil {
				t.Fatalf("rendered config is not JSON: %v\n%s", err, got)
			}
			if err := json.Unmarshal(want, &wantV); err != nil {
				t.Fatalf("golden is not JSON: %v", err)
			}
			gotCanon, _ := json.Marshal(gotV)
			wantCanon, _ := json.Marshal(wantV)
			if string(gotCanon) != string(wantCanon) {
				t.Fatalf("rendered config differs from golden:\ngot:  %s\nwant: %s", gotCanon, wantCanon)
			}
			if tc.hasScript && !strings.Contains(string(got), tc.script) {
				t.Fatalf("rendered config does not point at the script %q:\n%s", tc.script, got)
			}
		})
	}
}

func TestRenderFreshRejectsUnknownHarness(t *testing.T) {
	t.Parallel()
	if _, err := hooks.RenderFresh("opencode", "/data/hooks/opencode/fleet-goal-hook.py"); err == nil {
		t.Fatal("RenderFresh(opencode) succeeded, want an error: only claude, codex, muse and cursor have a defined shape")
	}
}

func TestCommandIsFixedAndPointsAtScript(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"claude", "codex", "muse", "cursor"} {
		cmd := hooks.Command(h, "/data/hooks/"+h+"/fleet-goal-hook.py")
		if !strings.Contains(cmd, "/data/hooks/"+h+"/fleet-goal-hook.py") {
			t.Errorf("harness %s: command %q does not point at the script", h, cmd)
		}
		if !strings.Contains(cmd, " "+h) {
			t.Errorf("harness %s: command %q does not name the harness", h, cmd)
		}
		if !strings.Contains(cmd, "command -v python3") {
			t.Errorf("harness %s: command %q has no fail-open python3 guard", h, cmd)
		}
	}
}

func TestIsFleetCommand(t *testing.T) {
	t.Parallel()
	dir := "/data/hooks/claude"
	fleet := hooks.Command("claude", dir+"/fleet-goal-hook.py")
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{"fleet command", fleet, true},
		{"other hook command", "exec \"$r/.claude/hooks/goal-continue.sh\"", false},
		{"empty", "", false},
		{"prefix lookalike", "/data/hooks/claude-evil/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hooks.IsFleetCommand(tc.cmd, dir); got != tc.want {
				t.Errorf("IsFleetCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestTrust(t *testing.T) {
	t.Parallel()
	cases := []struct {
		harness string
		pending bool
		approve string
	}{
		// Claude Code runs user hooks without approval (01d).
		{"claude", false, ""},
		// Cursor has no hook approval step (01d3).
		{"cursor", false, ""},
		// Codex runs a new or changed hook only after /hooks approval (01d).
		{"codex", true, "/hooks"},
		// Muse runs plugin hooks only after plugins approve (01d).
		{"muse", true, "muse plugins approve fleet-goal-hooks"},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			t.Parallel()
			trust, approve := hooks.Trust(tc.harness)
			if (trust == typev1.HookTrustState_HOOK_TRUST_STATE_PENDING) != tc.pending {
				t.Errorf("Trust(%s) = %v, want pending=%v", tc.harness, trust, tc.pending)
			}
			if tc.pending && !strings.Contains(approve, tc.approve) {
				t.Errorf("Trust(%s) approve %q does not name %q", tc.harness, approve, tc.approve)
			}
			if !tc.pending && approve != "" {
				t.Errorf("Trust(%s) approve = %q, want empty", tc.harness, approve)
			}
		})
	}
}
