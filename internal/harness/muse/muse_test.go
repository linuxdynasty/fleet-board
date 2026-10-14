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
// matches the claims, the USAGE and READY claims bring a fixture of each
// version, and every fixture parses or yields unknown, never fails. Every
// version stays UNVERIFIED_PARSING: USAGE and the trust half of READY are
// fixture-verified, and PROMPT, IDENTITY and CONDITION (plus prompt
// detection inside READY) are later slices.
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
	// 1.4.4 captured three trust shapes (the update banner, a plain folder
	// and a git root); the other versions captured one each.
	trustFixtures := map[string][]string{
		"1.4.4": {"trust-workspace.txt", "trust-workspace-plain-folder.txt", "trust-workspace-git-root.txt"},
		"1.4.6": {"trust-workspace.txt"},
		"1.4.7": {"trust-workspace.txt"},
	}
	var versions []harness.VersionCase
	for _, v := range Versions {
		var trust []harness.Fixture
		for _, name := range trustFixtures[v] {
			trust = append(trust, harness.Fixture{Name: name, Screen: readData(t, v+"/"+name)})
		}
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
					Claim:    harness.FeatureClaim{Supported: true},
					Parse:    ready,
					Fixtures: trust,
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
