package opencode

import (
	"os"
	"path/filepath"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

func readFixture(t *testing.T, version, name string) string {
	t.Helper()
	//nolint:gosec // G304: a fixture under the package's own testdata, named by the test, test code only
	raw, err := os.ReadFile(filepath.Join("testdata", version, name))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", version, name, err)
	}
	return string(raw)
}

func outcomeOf(outcome typev1.ParseOutcome) harness.ParseResult {
	switch outcome {
	case typev1.ParseOutcome_PARSE_OUTCOME_OK,
		typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
		return harness.ParseOK
	default:
		return harness.ParseUnknown
	}
}

// TestConform runs the shared adapter check (row 8's conformance): the
// golden table matches the claims, USAGE brings the stats fixtures,
// IDENTITY brings the providers and model-line fixtures of each version,
// and READY brings the idle-prompt, menu and dialog fixtures of each
// version, and every fixture parses or yields unknown, never fails. Both
// versions stay UNVERIFIED_PARSING: USAGE (spend), IDENTITY (providers,
// model line) and READY (idle prompt with its input state) are
// fixture-verified, and PROMPT and CONDITION are later slices. OpenCode
// has no usage window, no folder-trust screen and no /goal (01b).
func TestConform(t *testing.T) {
	usage := func(screen string) harness.ParseResult {
		return outcomeOf(ParseSpend(screen).ParseOutcome)
	}
	identity := func(screen string) harness.ParseResult {
		if got := outcomeOf(ParseProviders(screen).ParseOutcome); got == harness.ParseOK {
			return got
		}
		return outcomeOf(ParseModelLine(screen).ParseOutcome)
	}
	ready := func(screen string) harness.ParseResult {
		return outcomeOf(ParseReady(screen).ParseOutcome)
	}
	versions := []string{"1.18.34", "1.19.0"}
	usageFixtures := func() []harness.Fixture {
		var out []harness.Fixture
		for _, v := range versions {
			out = append(out,
				harness.Fixture{Name: v + "/stats.txt", Screen: readFixture(t, v, "stats.txt")},
				// A TUI dialog with no stats table yields unknown, never a number.
				harness.Fixture{Name: v + "/tui-status-dialog.txt", Screen: readFixture(t, v, "tui-status-dialog.txt")},
			)
		}
		return out
	}
	identityFixtures := func() []harness.Fixture {
		var out []harness.Fixture
		for _, v := range versions {
			out = append(out,
				harness.Fixture{Name: v + "/providers-list.txt", Screen: readFixture(t, v, "providers-list.txt")},
				harness.Fixture{Name: v + "/tui-ready.txt", Screen: readFixture(t, v, "tui-ready.txt")},
				// A stats table names no account, so identity yields unknown.
				harness.Fixture{Name: v + "/stats.txt", Screen: readFixture(t, v, "stats.txt")},
			)
		}
		return out
	}
	// The idle prompt screens prove the ready pattern of each version; the
	// menu, dialog and CLI screens prove the parser stays silent on them.
	readyByVersion := map[string][]string{
		"1.18.34": {
			"tui-ready.txt", "ready-new-folder.txt",
			"tui-slash-menu-first.txt", "tui-slash-menu-last.txt",
			"tui-status-dialog.txt", "tui-help-dialog.txt",
			"stats.txt", "providers-list.txt",
		},
		"1.19.0": {
			"tui-ready.txt", "tui-ready-ansi.txt",
			"tui-ready-input-has-draft.txt", "tui-ready-input-has-draft-ansi.txt",
			"tui-slash-menu-first.txt", "tui-slash-menu-last.txt",
			"tui-status-dialog.txt", "stats.txt", "providers-list.txt",
		},
	}
	readyFixtures := func(v string) []harness.Fixture {
		var out []harness.Fixture
		for _, name := range readyByVersion[v] {
			out = append(out, harness.Fixture{Name: v + "/" + name, Screen: readFixture(t, v, name)})
		}
		return out
	}
	decl := harness.Declaration{
		Harness: "opencode",
		Versions: []harness.VersionCase{
			{
				Version: "1.18.34",
				Support: harness.UnverifiedParsing,
				Features: map[harness.Feature]harness.FeatureCase{
					harness.FeatureUsage: {
						Claim:    harness.FeatureClaim{Supported: true},
						Parse:    usage,
						Fixtures: usageFixtures(),
					},
					harness.FeatureIdentity: {
						Claim:    harness.FeatureClaim{Supported: true},
						Parse:    identity,
						Fixtures: identityFixtures(),
					},
					harness.FeatureReady: {
						Claim:    harness.FeatureClaim{Supported: true},
						Parse:    ready,
						Fixtures: readyFixtures("1.18.34"),
					},
				},
			},
			{
				Version: "1.19.0",
				Support: harness.UnverifiedParsing,
				Features: map[harness.Feature]harness.FeatureCase{
					harness.FeatureUsage: {
						Claim:    harness.FeatureClaim{Supported: true},
						Parse:    usage,
						Fixtures: usageFixtures(),
					},
					harness.FeatureIdentity: {
						Claim:    harness.FeatureClaim{Supported: true},
						Parse:    identity,
						Fixtures: identityFixtures(),
					},
					harness.FeatureReady: {
						Claim:    harness.FeatureClaim{Supported: true},
						Parse:    ready,
						Fixtures: readyFixtures("1.19.0"),
					},
				},
			},
		},
		Golden: readFixture(t, ".", "support-table.txt"),
	}
	harness.Conform(t, decl)
	harness.ConformDirs(t, "testdata", decl)
}
