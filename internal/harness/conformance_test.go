package harness_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/harness"
)

func okParse(string) harness.ParseResult      { return harness.ParseOK }
func unknownParse(string) harness.ParseResult { return harness.ParseUnknown }
func failedParse(string) harness.ParseResult  { return harness.ParseFailed }

// stubDeclaration is a harness whose claims hold: one SUPPORTED version,
// every feature supported, each with a fixture that parses, plus one
// malformed screen that cleanly yields unknown.
func stubDeclaration() harness.Declaration {
	features := map[harness.Feature]harness.FeatureCase{}
	for _, f := range harness.Features {
		features[f] = harness.FeatureCase{
			Claim: harness.FeatureClaim{Supported: true},
			Parse: okParse,
			Fixtures: []harness.Fixture{
				{Name: strings.ToLower(string(f)) + ".txt", Screen: "a fine " + string(f) + " screen"},
			},
		}
	}
	usage := features[harness.FeatureUsage]
	usage.Fixtures = append(usage.Fixtures, harness.Fixture{
		Name:   "usage-truncated.txt",
		Screen: "garbage \x00 truncated",
	})
	usage.Parse = func(screen string) harness.ParseResult {
		if strings.HasPrefix(screen, "a fine") {
			return harness.ParseOK
		}
		return harness.ParseUnknown
	}
	features[harness.FeatureUsage] = usage
	return harness.Declaration{
		Harness: "stub",
		Versions: []harness.VersionCase{{
			Version:  "1.0.0",
			Support:  harness.Supported,
			Features: features,
		}},
		Golden: "harness stub\n" +
			"version support USAGE IDENTITY PROMPT READY CONDITION\n" +
			"1.0.0 SUPPORTED supported supported supported supported supported\n",
	}
}

func TestConformPassesForHoldingClaims(t *testing.T) {
	t.Parallel()
	harness.Conform(t, stubDeclaration())
}

func TestSupportTableIsTheGoldenText(t *testing.T) {
	t.Parallel()
	d := stubDeclaration()
	if got := harness.SupportTable(d); got != d.Golden {
		t.Errorf("SupportTable() =\n%s\nwant\n%s", got, d.Golden)
	}
}

func TestCheckRefuses(t *testing.T) {
	t.Parallel()
	base := stubDeclaration()
	unsupportedVersion := base.Versions[0]
	unsupportedVersion.Support = harness.UnverifiedParsing
	unsupportedVersion.Features[harness.FeaturePrompt] = harness.FeatureCase{
		Claim:    harness.FeatureClaim{ParsingLive: true},
		Parse:    unknownParse,
		Fixtures: []harness.Fixture{{Name: "prompt-new.txt", Screen: "new prompt screen"}},
	}

	tests := []struct {
		name    string
		mutate  func(*harness.Declaration)
		wantErr []string
	}{
		{
			"supported feature with no fixture",
			func(d *harness.Declaration) {
				noFixture := d.Versions[0].Features[harness.FeatureReady]
				noFixture.Fixtures = nil
				d.Versions[0].Features[harness.FeatureReady] = noFixture
			},
			[]string{"1.0.0", "READY", "no fixture"},
		},
		{
			"supported version with an unsupported feature",
			func(d *harness.Declaration) {
				unsupported := d.Versions[0].Features[harness.FeaturePrompt]
				unsupported.Claim.Supported = false
				d.Versions[0].Features[harness.FeaturePrompt] = unsupported
			},
			[]string{"1.0.0", "PROMPT", "SUPPORTED"},
		},
		{
			"supported version missing a feature entry",
			func(d *harness.Declaration) {
				delete(d.Versions[0].Features, harness.FeatureCondition)
			},
			[]string{"1.0.0", "CONDITION", "SUPPORTED"},
		},
		{
			"unverified version may leave a feature live-only",
			func(d *harness.Declaration) {
				d.Versions[0] = unsupportedVersion
				d.Golden = "harness stub\n" +
					"version support USAGE IDENTITY PROMPT READY CONDITION\n" +
					"1.0.0 UNVERIFIED_PARSING supported supported live supported supported\n"
			},
			nil,
		},
		{
			"fixture whose parse fails",
			func(d *harness.Declaration) {
				broken := d.Versions[0].Features[harness.FeatureReady]
				broken.Parse = failedParse
				d.Versions[0].Features[harness.FeatureReady] = broken
			},
			[]string{"1.0.0", "READY", "ready.txt"},
		},
		{
			"fixture with no parser to run",
			func(d *harness.Declaration) {
				nilParse := d.Versions[0].Features[harness.FeatureReady]
				nilParse.Parse = nil
				d.Versions[0].Features[harness.FeatureReady] = nilParse
			},
			[]string{"1.0.0", "READY", "no parser"},
		},
		{
			"unknown feature name",
			func(d *harness.Declaration) {
				d.Versions[0].Features["USGAE"] = harness.FeatureCase{
					Claim:    harness.FeatureClaim{Supported: true},
					Parse:    okParse,
					Fixtures: []harness.Fixture{{Name: "x.txt", Screen: "x"}},
				}
			},
			[]string{"USGAE", "unknown feature"},
		},
		{
			"golden table mismatch",
			func(d *harness.Declaration) {
				d.Golden = "harness stub\nstale table\n"
			},
			[]string{"golden", "support table"},
		},
		{
			"no versions declared",
			func(d *harness.Declaration) {
				d.Versions = nil
				d.Golden = "harness stub\n" +
					"version support USAGE IDENTITY PROMPT READY CONDITION\n"
			},
			[]string{"no versions"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := stubDeclaration()
			tc.mutate(&d)
			problems := harness.Check(d)
			if tc.wantErr == nil {
				if len(problems) != 0 {
					t.Errorf("Check() = %q, want no problems", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("Check() found nothing, want %q", tc.wantErr)
			}
			joined := strings.Join(problems, "\n")
			for _, want := range tc.wantErr {
				if !strings.Contains(joined, want) {
					t.Errorf("Check() = %q, want it to mention %q", problems, want)
				}
			}
		})
	}
}
