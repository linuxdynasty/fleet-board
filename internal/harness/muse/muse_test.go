package muse

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
// matches the claims, the USAGE claim brings a fixture of each version,
// and every fixture parses or yields unknown, never fails. Every
// version stays UNVERIFIED_PARSING: only USAGE is fixture-verified, and
// PROMPT, READY, IDENTITY and CONDITION are later slices.
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
	var versions []harness.VersionCase
	for _, v := range Versions {
		versions = append(versions, harness.VersionCase{
			Version: v,
			Support: harness.UnverifiedParsing,
			Features: map[harness.Feature]harness.FeatureCase{
				harness.FeatureUsage: {
					Claim: harness.FeatureClaim{Supported: true},
					Parse: parse,
					Fixtures: []harness.Fixture{
						{Name: "usage.txt", Screen: readData(t, v+"/usage.txt")},
					},
				},
			},
		})
	}
	harness.Conform(t, harness.Declaration{
		Harness:  "muse",
		Versions: versions,
		Golden:   readData(t, "support-table.txt"),
	})
}
