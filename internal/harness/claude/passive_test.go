package claude

import (
	"strings"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The banner shape below is the one 01 D3 records, seen 2026-09-30:
// "You've used 96% of your weekly limit · resets 10pm
// (America/New_York)". These are inline strings, not testdata fixtures:
// no live capture of a near-limit banner exists, so passive stays out of
// the conformance golden table until a live probe records one.
func bannerScreen(banner string) string {
	return "Welcome back.\n\n" + banner + "\n\n? for shortcuts\n"
}

func TestParsePassiveBanner(t *testing.T) {
	t.Parallel()
	readTime := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	got := ParsePassive(bannerScreen("You've used 96% of your weekly limit · resets 10pm (Etc/UTC)"), readTime)
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if len(got.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(got.Buckets))
	}
	b := got.Buckets[0]
	if b.GetKey() != "weekly_all" {
		t.Errorf("key = %q, want weekly_all", b.GetKey())
	}
	if b.GetKind() != typev1.WindowKind_WINDOW_KIND_WEEKLY {
		t.Errorf("kind = %v, want WEEKLY", b.GetKind())
	}
	if b.GetPercentUsed().GetValue() != 96 {
		t.Errorf("percent = %v, want 96", b.GetPercentUsed())
	}
	wantReset := time.Date(2026, time.September, 30, 22, 0, 0, 0, time.UTC)
	if rt := b.GetResetsTime().GetResolvedTime().AsTime(); !rt.Equal(wantReset) {
		t.Errorf("reset = %v, want %v", rt, wantReset)
	}
	if shown := b.GetResetsTime().GetOriginalText(); shown != "resets 10pm (Etc/UTC)" {
		t.Errorf("reset text = %q, want the banner words", shown)
	}
}

func TestParsePassiveBannerMinutes(t *testing.T) {
	t.Parallel()
	readTime := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	got := ParsePassive(bannerScreen("You've used 41% of your weekly limit · resets 10:30pm (Etc/UTC)"), readTime)
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Buckets[0].GetPercentUsed().GetValue() != 41 {
		t.Errorf("percent = %v, want 41", got.Buckets[0].GetPercentUsed())
	}
	wantReset := time.Date(2026, time.September, 30, 22, 30, 0, 0, time.UTC)
	if rt := got.Buckets[0].GetResetsTime().GetResolvedTime().AsTime(); !rt.Equal(wantReset) {
		t.Errorf("reset = %v, want %v", rt, wantReset)
	}
}

func TestParsePassiveSpecZone(t *testing.T) {
	t.Parallel()
	readTime := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	got := ParsePassive(bannerScreen("You've used 96% of your weekly limit · resets 10pm (America/New_York)"), readTime)
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Buckets[0].GetPercentUsed().GetValue() != 96 {
		t.Errorf("percent = %v, want 96", got.Buckets[0].GetPercentUsed())
	}
	if got.Buckets[0].GetResetsTime().GetResolvedTime() == nil {
		t.Errorf("no resolved reset for the spec zone")
	}
	if zone := got.Buckets[0].GetResetsTime().GetResolvedWithTimezone(); zone != "America/New_York" {
		t.Errorf("zone = %q, want America/New_York", zone)
	}
}

func TestParsePassiveNoZoneFallsBack(t *testing.T) {
	t.Parallel()
	got := ParsePassive(bannerScreen("You've used 12% of your weekly limit · resets 10pm"), time.Now())
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Buckets[0].GetPercentUsed().GetValue() != 12 {
		t.Errorf("percent = %v, want 12", got.Buckets[0].GetPercentUsed())
	}
	if got.Buckets[0].GetResetsTime() == nil {
		t.Errorf("no reset without a zone name")
	}
}

func TestParsePassiveNoBannerFails(t *testing.T) {
	t.Parallel()
	for _, screen := range []string{
		"",
		"a ready prompt with no banner\n❯ \n",
		"What's contributing\n  96% used by this machine\n",
	} {
		got := ParsePassive(screen, time.Now())
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("outcome = %v for %q, want FAILED", got.ParseOutcome, screen)
		}
		if len(got.Buckets) != 0 {
			t.Errorf("buckets = %d for %q, want none", len(got.Buckets), screen)
		}
	}
}

func TestParsePassiveBadPercentIsUnknown(t *testing.T) {
	t.Parallel()
	got := ParsePassive(bannerScreen("You've used 140% of your weekly limit · resets 10pm (Etc/UTC)"), time.Now())
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if len(got.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1 unknown bucket", len(got.Buckets))
	}
	if got.Buckets[0].GetPercentUsed().GetValue() != 0 {
		t.Errorf("percent reads %v, want unknown never 140", got.Buckets[0].GetPercentUsed())
	}
	if got.Buckets[0].GetPercentUsed().GetUnknown() == nil {
		t.Errorf("percent is a number, want unknown")
	}
}

func TestParsePassiveBadResetStaysUnknown(t *testing.T) {
	t.Parallel()
	got := ParsePassive(bannerScreen("You've used 96% of your weekly limit · resets soon"), time.Now())
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if got.Buckets[0].GetPercentUsed().GetValue() != 96 {
		t.Errorf("percent = %v, want the 96 the banner shows", got.Buckets[0].GetPercentUsed())
	}
	if got.Buckets[0].GetResetsTime() != nil {
		t.Errorf("reset reads %v, want unknown never guessed", got.Buckets[0].GetResetsTime())
	}
}

func TestParsePassiveOversizedFails(t *testing.T) {
	t.Parallel()
	got := ParsePassive(strings.Repeat("x", 65*1024), time.Now())
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
		t.Errorf("outcome = %v, want FAILED past the screen cap", got.ParseOutcome)
	}
}
