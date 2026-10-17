package claude

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

// TestConform runs the shared adapter check (row 8): the golden table
// matches the claims, the USAGE, READY and CONDITION claims bring a fixture
// of this version, and every fixture parses or yields unknown, never fails.
// 2.1.296 stays UNVERIFIED_PARSING: USAGE, the trust half of READY and the
// interrupt CONDITION are fixture-verified, and PROMPT and IDENTITY (plus
// prompt detection inside READY) are later slices.
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
	condition := func(screen string) harness.ParseResult {
		switch ParseCondition(screen).ParseOutcome {
		case typev1.ParseOutcome_PARSE_OUTCOME_OK,
			typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
			return harness.ParseOK
		default:
			return harness.ParseUnknown
		}
	}
	var versions []harness.VersionCase
	for _, v := range Versions {
		versions = append(versions, harness.VersionCase{
			Version: v,
			Support: harness.UnverifiedParsing,
			Features: map[harness.Feature]harness.FeatureCase{
				harness.FeatureUsage: {
					Claim: harness.FeatureClaim{Supported: true},
					Parse: usage,
					Fixtures: []harness.Fixture{
						{Name: "usage.txt", Screen: readData(t, v+"/usage.txt")},
					},
				},
				harness.FeatureReady: {
					Claim: harness.FeatureClaim{Supported: true},
					Parse: ready,
					Fixtures: []harness.Fixture{
						{Name: "trust-folder.txt", Screen: readData(t, v+"/trust-folder.txt")},
					},
				},
				harness.FeatureCondition: {
					Claim: harness.FeatureClaim{Supported: true},
					Parse: condition,
					Fixtures: []harness.Fixture{
						{Name: "interrupted.txt", Screen: readData(t, v+"/interrupted.txt")},
					},
				},
			},
		})
	}
	decl := harness.Declaration{
		Harness:  "claude",
		Versions: versions,
		Golden:   readData(t, "support-table.txt"),
	}
	harness.Conform(t, decl)
	harness.ConformDirs(t, "testdata", decl)
}
