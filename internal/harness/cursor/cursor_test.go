package cursor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

func readData(t *testing.T, name string) string {
	t.Helper()
	//nolint:gosec // G304: a fixture under the package's own testdata, named by the test, test code only
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// TestConform runs the shared adapter check (row 8's conformance, which row
// 30's adapter calls too): the golden table matches the claims, a supported
// feature brings a fixture of this version, and every fixture parses or
// yields unknown, never fails. 2026.10.01 stays UNVERIFIED_PARSING: USAGE,
// IDENTITY, READY and the stall CONDITION are fixture-verified, and PROMPT
// is a later slice.
func TestConform(t *testing.T) {
	usage := func(screen string) harness.ParseResult {
		switch ParseUsage(screen, time.Now()).ParseOutcome {
		case typev1.ParseOutcome_PARSE_OUTCOME_OK,
			typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
			return harness.ParseOK
		default:
			return harness.ParseUnknown
		}
	}
	ready := func(screen string) harness.ParseResult {
		switch ParseReady(screen).ParseOutcome {
		case typev1.ParseOutcome_PARSE_OUTCOME_OK,
			typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
			return harness.ParseOK
		default:
			return harness.ParseUnknown
		}
	}
	identity := func(screen string) harness.ParseResult {
		switch ParseIdentity(screen).ParseOutcome {
		case typev1.ParseOutcome_PARSE_OUTCOME_OK,
			typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
			return harness.ParseOK
		default:
			return harness.ParseUnknown
		}
	}
	condition := func(screen string) harness.ParseResult {
		switch ParseCondition(screen).ParseOutcome {
		case typev1.ParseOutcome_PARSE_OUTCOME_OK,
			typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
			return harness.ParseOK
		default:
			return harness.ParseUnknown
		}
	}
	decl := harness.Declaration{
		Harness: "cursor",
		Versions: []harness.VersionCase{
			{
				Version: Version,
				Support: harness.UnverifiedParsing,
				Features: map[harness.Feature]harness.FeatureCase{
					harness.FeatureUsage: {
						Claim: harness.FeatureClaim{Supported: true},
						Parse: usage,
						Fixtures: []harness.Fixture{
							{Name: "usage.txt", Screen: readData(t, "2026.10.01/usage.txt")},
							{Name: "usage-not-shown.txt", Screen: readData(t, "2026.10.01/usage-not-shown.txt")},
						},
					},
					harness.FeatureIdentity: {
						Claim: harness.FeatureClaim{Supported: true},
						Parse: identity,
						Fixtures: []harness.Fixture{
							{Name: "status.txt", Screen: readData(t, "2026.10.01/status.txt")},
							{Name: "about.txt", Screen: readData(t, "2026.10.01/about.txt")},
							{Name: "identity-truncated.txt", Screen: readData(t, "2026.10.01/identity-truncated.txt")},
						},
					},
					harness.FeatureCondition: {
						Claim: harness.FeatureClaim{Supported: true},
						Parse: condition,
						Fixtures: []harness.Fixture{
							{Name: "goal-paused.txt", Screen: readData(t, "2026.10.01/goal-paused.txt")},
						},
					},
					harness.FeatureReady: {
						Claim: harness.FeatureClaim{Supported: true},
						Parse: ready,
						Fixtures: []harness.Fixture{
							{Name: "ready-ansi.txt", Screen: readData(t, "2026.10.01/ready-ansi.txt")},
							{Name: "ready-followup-placeholder.ansi.txt", Screen: readData(t, "2026.10.01/ready-followup-placeholder.ansi.txt")},
							{Name: "ready-input-has-draft.ansi.txt", Screen: readData(t, "2026.10.01/ready-input-has-draft.ansi.txt")},
							{Name: "ready-followup-placeholder.txt", Screen: readData(t, "2026.10.01/ready-followup-placeholder.txt")},
							{Name: "ready-input-has-draft.txt", Screen: readData(t, "2026.10.01/ready-input-has-draft.txt")},
							{Name: "ready-trusted-child.txt", Screen: readData(t, "2026.10.01/ready-trusted-child.txt")},
							{Name: "usage-not-shown.txt", Screen: readData(t, "2026.10.01/usage-not-shown.txt")},
							{Name: "trust-workspace.txt", Screen: readData(t, "2026.10.01/trust-workspace.txt")},
							{Name: "usage.txt", Screen: readData(t, "2026.10.01/usage.txt")},
							{Name: "ready-truncated.txt", Screen: readData(t, "2026.10.01/ready-truncated.txt")},
						},
					},
				},
			},
		},
		Golden: readData(t, "support-table.txt"),
	}
	harness.Conform(t, decl)
	harness.ConformDirs(t, "testdata", decl)
}
