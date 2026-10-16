package probe

import (
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const passiveBannerScreen = "Welcome back.\n\nYou've used 96% of your weekly limit · resets 10pm (Etc/UTC)\n\n? for shortcuts\n"

// TestBuildPassiveReading turns the spec-recorded Claude banner into a
// PASSIVE reading: buckets ride through, the probe id stays empty (the
// contract sets it only for probe readings), and ids echo the pass.
func TestBuildPassiveReading(t *testing.T) {
	t.Parallel()
	pane := &typev1.PaneKey{Server: "developer", ServerStartId: "s", PaneId: "%3"}
	accountKey := &typev1.AccountKey{Value: "key-9"}
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	r, ok := BuildPassiveReading("claude", passiveBannerScreen, now, pane, accountKey, &typev1.Harness{Name: "claude"})
	if !ok {
		t.Fatalf("BuildPassiveReading(claude) = false")
	}
	if r.GetSource() != typev1.UsageSource_USAGE_SOURCE_PASSIVE {
		t.Errorf("source = %v, want PASSIVE", r.GetSource())
	}
	if r.GetProbeId().GetValue() != "" {
		t.Errorf("probe id = %q, want empty on a passive reading", r.GetProbeId().GetValue())
	}
	if r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Errorf("parse = %v, want OK", r.GetParse())
	}
	if len(r.GetBuckets()) != 1 || r.GetBuckets()[0].GetPercentUsed().GetValue() != 96 {
		t.Errorf("buckets = %v, want the banner's 96", r.GetBuckets())
	}
	if r.GetPane() != pane || r.GetAccountKey() != accountKey {
		t.Errorf("ids do not echo the observed pane")
	}
	if r.GetParserVersion() == "" {
		t.Errorf("no parser version")
	}
	if r.GetShown().GetRawScreen() == "" {
		t.Errorf("passive reading carries no raw screen")
	}
}

// TestBuildPassiveReadingUnknownHarness refuses harnesses with no passive
// parser instead of guessing a reading.
func TestBuildPassiveReadingUnknownHarness(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"codex", "muse", "cursor", "opencode"} {
		if _, ok := BuildPassiveReading(h, passiveBannerScreen, time.Now(), nil, nil, nil); ok {
			t.Errorf("BuildPassiveReading(%s) = true", h)
		}
	}
}

// TestBuildPassiveReadingNoBanner still reports a FAILED reading with its
// raw screen, so the caller sees the parse failed and sends nothing.
func TestBuildPassiveReadingNoBanner(t *testing.T) {
	t.Parallel()
	r, ok := BuildPassiveReading("claude", "a ready prompt with no banner", time.Now(), nil, nil, nil)
	if !ok {
		t.Fatalf("BuildPassiveReading = false")
	}
	if r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
		t.Errorf("parse = %v, want FAILED", r.GetParse())
	}
	if len(r.GetBuckets()) != 0 {
		t.Errorf("buckets = %d, want none on FAILED", len(r.GetBuckets()))
	}
}

// TestPassiveCache emits only on change (03b D3): a repainted status line
// that reads the same costs nothing; a moved percent or reset emits; a
// failed parse never emits and never poisons the last good value.
func TestPassiveCache(t *testing.T) {
	t.Parallel()
	bucket := func(percent uint32, unknown typev1.UnknownReason, hasReset bool) *typev1.LimitBucket {
		b := &typev1.LimitBucket{Key: "weekly_all", Kind: typev1.WindowKind_WINDOW_KIND_WEEKLY}
		if unknown != typev1.UnknownReason_UNKNOWN_REASON_UNSPECIFIED {
			b.PercentUsed = &typev1.Percent{Kind: &typev1.Percent_Unknown{Unknown: &typev1.PercentUnknown{Reason: unknown}}}
		} else {
			b.PercentUsed = &typev1.Percent{Kind: &typev1.Percent_Value{Value: percent}}
		}
		if hasReset {
			b.ResetsTime = &typev1.ResetTime{OriginalText: "resets 10pm"}
		}
		return b
	}
	reading := func(outcome typev1.ParseOutcome, b *typev1.LimitBucket) *agentv1.UsageReading {
		return &agentv1.UsageReading{Parse: outcome, Buckets: []*typev1.LimitBucket{b}}
	}
	ok96 := reading(typev1.ParseOutcome_PARSE_OUTCOME_OK, bucket(96, typev1.UnknownReason_UNKNOWN_REASON_UNSPECIFIED, true))
	failed := reading(typev1.ParseOutcome_PARSE_OUTCOME_FAILED, nil)

	var c PassiveCache
	if !c.ShouldEmit("key-1", ok96) {
		t.Fatalf("first reading does not emit")
	}
	if c.ShouldEmit("key-1", ok96) {
		t.Errorf("identical repeat emits")
	}
	if !c.ShouldEmit("key-1", reading(typev1.ParseOutcome_PARSE_OUTCOME_OK, bucket(97, typev1.UnknownReason_UNKNOWN_REASON_UNSPECIFIED, true))) {
		t.Errorf("moved percent does not emit")
	}
	moved := reading(typev1.ParseOutcome_PARSE_OUTCOME_OK, bucket(97, typev1.UnknownReason_UNKNOWN_REASON_UNSPECIFIED, true))
	moved.GetBuckets()[0].GetResetsTime().OriginalText = "resets 11pm"
	if !c.ShouldEmit("key-1", moved) {
		t.Errorf("moved reset does not emit")
	}
	if !c.ShouldEmit("key-2", ok96) {
		t.Errorf("other account does not emit")
	}
	if c.ShouldEmit("key-1", failed) {
		t.Errorf("FAILED parse emits")
	}
	if c.ShouldEmit("key-1", nil) {
		t.Errorf("nil reading emits")
	}
	if !c.ShouldEmit("key-1", reading(typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL, bucket(0, typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED, false))) {
		t.Errorf("unknown after FAILED does not emit")
	}
}

// TestPassiveCacheConcurrent hammers one cache from several goroutines:
// emissions are deduplicated and nothing races (run with -race).
func TestPassiveCacheConcurrent(t *testing.T) {
	t.Parallel()
	var c PassiveCache
	done := make(chan bool, 8)
	for i := uint32(0); i < 8; i++ {
		go func(v uint32) {
			r := &agentv1.UsageReading{
				Parse: typev1.ParseOutcome_PARSE_OUTCOME_OK,
				Buckets: []*typev1.LimitBucket{{
					Key:         "weekly_all",
					PercentUsed: &typev1.Percent{Kind: &typev1.Percent_Value{Value: v % 2}},
				}},
			}
			c.ShouldEmit("key-race", r)
			done <- true
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
