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
// golden table matches the claims, USAGE brings the stats fixtures and
// IDENTITY brings the providers and model-line fixtures of each version,
// and every fixture parses or yields unknown, never fails. Both versions
// stay UNVERIFIED_PARSING: only USAGE (spend) and IDENTITY (providers,
// model line) are fixture-verified, and PROMPT, READY and CONDITION are
// later slices. OpenCode has no usage window and no /goal (01b).
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
	harness.Conform(t, harness.Declaration{
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
				},
			},
		},
		Golden: readFixture(t, ".", "support-table.txt"),
	})
}
