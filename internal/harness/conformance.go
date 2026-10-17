package harness

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Feature is one parser feature an adapter verifies against fixtures.
type Feature string

const (
	FeatureUsage     Feature = "USAGE"
	FeatureIdentity  Feature = "IDENTITY"
	FeaturePrompt    Feature = "PROMPT"
	FeatureReady     Feature = "READY"
	FeatureCondition Feature = "CONDITION"
)

// Features is every feature, in the order the support table lists them.
var Features = []Feature{
	FeatureUsage,
	FeatureIdentity,
	FeaturePrompt,
	FeatureReady,
	FeatureCondition,
}

// Support is a harness version's support state (ADR 0018): SUPPORTED means
// fixtures exist and every feature is verified; UNVERIFIED_PARSING means the
// parser has only read live screens; UNSUPPORTED means the version is known
// but not parsed (for example /goal is missing).
type Support string

const (
	Supported         Support = "SUPPORTED"
	UnverifiedParsing Support = "UNVERIFIED_PARSING"
	Unsupported       Support = "UNSUPPORTED"
)

// FeatureClaim is what an adapter says about one feature on one version:
// verified against fixtures (Supported), or only read live without a
// PARSE_FAILED since the version appeared (ParsingLive).
type FeatureClaim struct {
	Supported   bool
	ParsingLive bool
}

// ParseResult is how one recorded screen ends in an adapter's parser: parsed,
// or cleanly unknown with the redacted raw screen attached. Anything else is
// a guess, and the conformance check refuses it.
type ParseResult string

const (
	ParseOK      ParseResult = "parsed"
	ParseUnknown ParseResult = "unknown"
	ParseFailed  ParseResult = "failed"
)

// Fixture is one recorded screen: its file name (for failure messages) and
// its text. The adapter's own test reads the files under its
// testdata/<version>/ and passes the contents here.
type Fixture struct {
	Name   string
	Screen string
}

// FeatureCase is one feature's claim, its parser wrapped for one screen,
// and the fixtures that prove the claim on one version.
type FeatureCase struct {
	Claim    FeatureClaim
	Parse    func(screen string) ParseResult
	Fixtures []Fixture
}

// VersionCase is one version's support state and its per-feature cases.
type VersionCase struct {
	Version  string
	Support  Support
	Features map[Feature]FeatureCase
}

// Declaration is what one adapter claims: its harness name, one case per
// version it supports, and the committed golden support table its test keeps
// next to its fixtures. A change to the claims is a change to that file, so
// it shows up as a diff in review.
type Declaration struct {
	Harness  string
	Versions []VersionCase
	Golden   string
}

// SupportTable renders a declaration as the canonical support table: one
// header line naming the harness, one column header line, one line per
// version. A supported feature renders "supported", a live-only one "live",
// anything else "-".
func SupportTable(d Declaration) string {
	var b strings.Builder
	b.WriteString("harness " + d.Harness + "\n")
	b.WriteString("version support")
	for _, f := range Features {
		b.WriteString(" " + string(f))
	}
	b.WriteString("\n")
	for _, v := range d.Versions {
		b.WriteString(v.Version + " " + string(v.Support))
		for _, f := range Features {
			b.WriteString(" " + cell(v.Features[f].Claim))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func cell(c FeatureClaim) string {
	switch {
	case c.Supported:
		return "supported"
	case c.ParsingLive:
		return "live"
	default:
		return "-"
	}
}

// Check runs the shared conformance rules over a declaration and returns one
// message per broken rule: the golden table must match, a SUPPORTED version
// must have every feature supported, a supported feature must bring a fixture
// of that version, every named feature must exist, and every fixture must
// parse or cleanly yield unknown, never fail. Empty means the adapter holds
// its claims.
func Check(d Declaration) []string {
	var problems []string
	if len(d.Versions) == 0 {
		return []string{"harness " + d.Harness + " declares no versions"}
	}
	if got := SupportTable(d); got != d.Golden {
		problems = append(problems, fmt.Sprintf(
			"harness %s: declared support does not match the golden support table:\ngot:\n%s\nwant:\n%s",
			d.Harness, got, d.Golden))
	}
	known := map[Feature]bool{}
	for _, f := range Features {
		known[f] = true
	}
	for _, v := range d.Versions {
		for f := range v.Features {
			if !known[f] {
				problems = append(problems, fmt.Sprintf(
					"harness %s version %s: unknown feature %q", d.Harness, v.Version, f))
			}
		}
		for _, f := range Features {
			fc, ok := v.Features[f]
			switch {
			case !ok || !fc.Claim.Supported:
				if v.Support == Supported {
					problems = append(problems, fmt.Sprintf(
						"harness %s version %s is SUPPORTED while feature %s is not supported",
						d.Harness, v.Version, f))
				}
			case len(fc.Fixtures) == 0:
				problems = append(problems, fmt.Sprintf(
					"harness %s version %s: feature %s is supported with no fixture of that version",
					d.Harness, v.Version, f))
			case fc.Parse == nil:
				problems = append(problems, fmt.Sprintf(
					"harness %s version %s: feature %s has fixtures but no parser to run",
					d.Harness, v.Version, f))
			default:
				for _, fix := range fc.Fixtures {
					if got := fc.Parse(fix.Screen); got != ParseOK && got != ParseUnknown {
						problems = append(problems, fmt.Sprintf(
							"harness %s version %s: feature %s fixture %s did not parse or yield unknown",
							d.Harness, v.Version, f, fix.Name))
					}
				}
			}
		}
	}
	return problems
}

// CheckFixtureDirs covers what Check cannot see: the fixture directories on
// disk. dirs holds the version directory names under the adapter's
// testdata/. It returns one message per gap: a directory with no declared
// version (new fixtures must arrive with their golden-table row, so the
// claim change is a reviewed diff), and a declared version with no
// directory (a claim with no fixtures behind it). Empty means every
// directory is declared and every declared version has its fixtures.
func CheckFixtureDirs(d Declaration, dirs []string) []string {
	declared := map[string]bool{}
	for _, v := range d.Versions {
		declared[v.Version] = true
	}
	onDisk := map[string]bool{}
	for _, dir := range dirs {
		onDisk[dir] = true
	}
	var problems []string
	for _, dir := range dirs {
		if !declared[dir] {
			problems = append(problems, fmt.Sprintf(
				"harness %s: fixture directory %s has no declared version", d.Harness, dir))
		}
	}
	for _, v := range d.Versions {
		if !onDisk[v.Version] {
			problems = append(problems, fmt.Sprintf(
				"harness %s version %s has no fixture directory", d.Harness, v.Version))
		}
	}
	return problems
}

// Conform is what every adapter's test calls, over every fixture of every
// version it supports (row 30's Cursor adapter too): it fails the test with
// one message per broken rule of Check.
func Conform(t *testing.T, d Declaration) {
	t.Helper()
	// Errorf, not Error: the telemetry-names gate reads every .Error( call
	// with a non-static argument in non-test code as a log event, and this
	// is test reporting, not telemetry.
	for _, p := range Check(d) {
		t.Errorf("%s", p)
	}
}

// ConformDirs is the fixture-directory half of the gate: every adapter's
// test calls it with its testdata/ path and the same declaration it passes
// to Conform. It fails the test with one message per gap of
// CheckFixtureDirs, so a new version's fixtures cannot land without their
// golden-table row. Like Conform this is test reporting, not telemetry.
func ConformDirs(t *testing.T, testdata string, d Declaration) {
	t.Helper()
	entries, err := os.ReadDir(testdata)
	if err != nil {
		t.Fatalf("read fixture directories in %s: %v", testdata, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	for _, p := range CheckFixtureDirs(d, dirs) {
		t.Errorf("%s", p)
	}
}
