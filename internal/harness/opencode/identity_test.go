package opencode

import (
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Both fixtures show an account with nothing signed in: zero credentials
// and no Environment section on this machine's captures.
func TestParseProvidersEmpty(t *testing.T) {
	for _, version := range []string{"1.18.34", "1.19.0"} {
		got := ParseProviders(readFixture(t, version, "providers-list.txt"))

		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
			t.Fatalf("%s: outcome = %v, want OK", version, got.ParseOutcome)
		}
		if got.Credentials != 0 {
			t.Errorf("%s: credentials = %d, want 0", version, got.Credentials)
		}
		if len(got.CredentialProviders) != 0 {
			t.Errorf("%s: providers = %v, want none", version, got.CredentialProviders)
		}
		if got.EnvSeen {
			t.Errorf("%s: env section seen, want none", version)
		}
	}
}

// The live shape on a machine with an env-var provider: the Credentials
// section plus an Environment section with one `name ENV_VAR` row. The env
// var's value is never shown, only its name.
func TestParseProvidersEnvSection(t *testing.T) {
	screen := "┌  Credentials ~/.local/share/opencode/auth.json\n" +
		"│\n" +
		"└  0 credentials\n" +
		"\n" +
		"┌  Environment\n" +
		"│\n" +
		"●  DeepSeek DEEPSEEK_API_KEY\n" +
		"│\n" +
		"└  1 environment variable\n"
	got := ParseProviders(screen)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if !got.EnvSeen || got.EnvCount != 1 {
		t.Fatalf("env = seen %v count %d, want seen true count 1", got.EnvSeen, got.EnvCount)
	}
	if len(got.EnvProviders) != 1 {
		t.Fatalf("env providers = %d, want 1", len(got.EnvProviders))
	}
	if got.EnvProviders[0].Name != "DeepSeek" || got.EnvProviders[0].Detail != "DEEPSEEK_API_KEY" {
		t.Errorf("env provider = %+v, want DeepSeek/DEEPSEEK_API_KEY", got.EnvProviders[0])
	}
}

// The live 1.19.3 providers fixture carries the Environment section this
// machine shows: zero credentials plus one `name ENV_VAR` row. The env
// var's value is never shown, only its name.
func TestParseProvidersEnvFixture(t *testing.T) {
	got := ParseProviders(readFixture(t, "1.19.3", "providers-list.txt"))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Credentials != 0 {
		t.Errorf("credentials = %d, want 0", got.Credentials)
	}
	if !got.EnvSeen || got.EnvCount != 1 {
		t.Fatalf("env = seen %v count %d, want seen true count 1", got.EnvSeen, got.EnvCount)
	}
	if len(got.EnvProviders) != 1 {
		t.Fatalf("env providers = %d, want 1", len(got.EnvProviders))
	}
	if got.EnvProviders[0].Name != "DeepSeek" || got.EnvProviders[0].Detail != "DEEPSEEK_API_KEY" {
		t.Errorf("env provider = %+v, want DeepSeek/DEEPSEEK_API_KEY", got.EnvProviders[0])
	}
}

// A `●` row in a shape the parser does not know is partial, never dropped:
// the count it shows is kept and the row is flagged by the outcome.
func TestParseProvidersUnknownRow(t *testing.T) {
	screen := "┌  Credentials ~/.local/share/opencode/auth.json\n" +
		"│\n" +
		"●\n" +
		"└  1 credentials\n"
	got := ParseProviders(screen)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if got.Credentials != 1 {
		t.Errorf("credentials = %d, want 1", got.Credentials)
	}
}

// A screen with no providers output fails cleanly.
func TestParseProvidersNoList(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":       "hello\n",
		"stats":         readFixture(t, "1.19.0", "stats.txt"),
		"status dialog": readFixture(t, "1.19.0", "tui-status-dialog.txt"),
	} {
		got := ParseProviders(screen)
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
	}
}

// The model line under the input box: the agent before the first `·`, then
// the model and provider as shown, then the variant after a second `·`
// when one is set. The model and provider are not split: only the harness
// knows where its model name ends.
func TestParseModelLineFixture(t *testing.T) {
	for version, want := range map[string]ModelLine{
		// "Build · Big Pickle OpenCode Zen".
		"1.18.34": {Agent: "Build", Selection: "Big Pickle OpenCode Zen"},
		// "Build · Big Pickle OpenCode".
		"1.19.0": {Agent: "Build", Selection: "Big Pickle OpenCode"},
		// "Build · DeepSeek V4 Pro DeepSeek": this machine exposes an
		// env-var provider, so the default model line names it.
		"1.19.3": {Agent: "Build", Selection: "DeepSeek V4 Pro DeepSeek"},
	} {
		got := ParseModelLine(readFixture(t, version, "tui-ready.txt"))

		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
			t.Fatalf("%s: outcome = %v, want OK", version, got.ParseOutcome)
		}
		if got.Agent != want.Agent || got.Selection != want.Selection {
			t.Errorf("%s: got %+v, want agent %q selection %q",
				version, got, want.Agent, want.Selection)
		}
		if got.Variant != "" {
			t.Errorf("%s: variant = %q, want empty", version, got.Variant)
		}
	}
}

// A model line with a variant ("Build · Kimi K3 Moonshot AI · high", 01b).
func TestParseModelLineVariant(t *testing.T) {
	screen := "┃  Build · Kimi K3 Moonshot AI · high\n"
	got := ParseModelLine(screen)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Agent != "Build" || got.Selection != "Kimi K3 Moonshot AI" || got.Variant != "high" {
		t.Errorf("got %+v, want Build / Kimi K3 Moonshot AI / high", got)
	}
}

// Color codes in a `-e` capture do not pollute the parse: the 1.19.0 ANSI
// ready screen reads the same model line as the plain one.
func TestParseModelLineANSI(t *testing.T) {
	got := ParseModelLine(readFixture(t, "1.19.0", "tui-ready-ansi.txt"))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Agent != "Build" || got.Selection != "Big Pickle OpenCode" {
		t.Errorf("got %+v, want Build / Big Pickle OpenCode", got)
	}
}

// A screen with no model line fails cleanly. (The status dialog is not one:
// it overlays the prompt box, so the model line still shows and parses.)
func TestParseModelLineMissing(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":   "hello\n",
		"stats":     readFixture(t, "1.19.0", "stats.txt"),
		"providers": readFixture(t, "1.19.0", "providers-list.txt"),
	} {
		got := ParseModelLine(screen)
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
	}
}
