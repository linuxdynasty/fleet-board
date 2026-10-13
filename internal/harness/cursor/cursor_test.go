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
// 30's adapter calls too): the golden table matches the claims, the USAGE
// claim brings a fixture of this version, and every fixture parses or
// yields unknown, never fails. 2026.10.01 stays UNVERIFIED_PARSING: only
// USAGE is fixture-verified, and PROMPT, READY, IDENTITY and CONDITION are
// later slices.
func TestConform(t *testing.T) {
	parse := func(screen string) harness.ParseResult {
		switch ParseUsage(screen, time.Now()).ParseOutcome {
		case typev1.ParseOutcome_PARSE_OUTCOME_OK,
			typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
			return harness.ParseOK
		default:
			return harness.ParseUnknown
		}
	}
	harness.Conform(t, harness.Declaration{
		Harness: "cursor",
		Versions: []harness.VersionCase{
			{
				Version: Version,
				Support: harness.UnverifiedParsing,
				Features: map[harness.Feature]harness.FeatureCase{
					harness.FeatureUsage: {
						Claim: harness.FeatureClaim{Supported: true},
						Parse: parse,
						Fixtures: []harness.Fixture{
							{Name: "usage.txt", Screen: readData(t, "2026.10.01/usage.txt")},
							{Name: "usage-not-shown.txt", Screen: readData(t, "2026.10.01/usage-not-shown.txt")},
						},
					},
				},
			},
		},
		Golden: readData(t, "support-table.txt"),
	})
}
