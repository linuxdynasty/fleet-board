package probe

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
	"github.com/linuxdynasty/fleet-board/internal/harness/claude"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// BuildPassiveReading assembles one USAGE_SOURCE_PASSIVE reading from a
// screen a pane the agent already observes shows (01 D3). It returns
// false only for a harness with no passive parser. A screen with no
// passive signal still returns a FAILED reading with the redacted raw
// screen, so the caller can tell a missed signal from an unsupported
// harness. The probe id stays empty: the contract sets it only for
// probe readings.
func BuildPassiveReading(harnessName, screen string, readTime time.Time, pane *typev1.PaneKey, accountKey *typev1.AccountKey, harnessMsg *typev1.Harness) (*agentv1.UsageReading, bool) {
	if harnessName != "claude" {
		return nil, false
	}
	u := claude.ParsePassive(screen, readTime)
	r := &agentv1.UsageReading{
		AccountKey:   accountKey,
		Harness:      harnessMsg,
		ReadTime:     timestamppb.New(readTime),
		Source:       typev1.UsageSource_USAGE_SOURCE_PASSIVE,
		Pane:         pane,
		Buckets:      u.Buckets,
		Parse:        u.ParseOutcome,
		ParseMessage: diag("claude", u.ParseOutcome),
		Shown: &typev1.ShownUsage{
			RawScreen:    scrub.Text(harness.StripControls(screen), scrub.Options{MaxBytes: 64 * 1024}),
			RawTruncated: len(screen) > 64*1024,
		},
	}
	r.ParserVersion = claude.PassiveParserVersion
	return r, true
}

// PassiveCache remembers the last passive values emitted per account, so
// a repainted status line that reads the same costs nothing (03b D3).
// Only a bucket whose percent or reset differs from the last emission
// goes out; the scheduler still never paces on a passive reading alone.
type PassiveCache struct {
	mu   sync.Mutex
	last map[string]string
}

// ShouldEmit reports whether r is worth sending for account: false for a
// nil reading or a FAILED parse (which also leaves the last good values
// alone), false when every bucket matches the last emission, true
// otherwise, remembering the new values.
func (c *PassiveCache) ShouldEmit(account string, r *agentv1.UsageReading) bool {
	if r == nil {
		return false
	}
	switch r.GetParse() {
	case typev1.ParseOutcome_PARSE_OUTCOME_OK,
		typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
	default:
		return false
	}
	sig := passiveSignature(r)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]string{}
	}
	if c.last[account] == sig {
		return false
	}
	c.last[account] = sig
	return true
}

// passiveSignature fingerprints the values 03b compares: each bucket's
// percent and reset, in key order.
func passiveSignature(r *agentv1.UsageReading) string {
	buckets := append([]*typev1.LimitBucket(nil), r.GetBuckets()...)
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].GetKey() < buckets[j].GetKey() })
	parts := make([]string, 0, len(buckets))
	for _, b := range buckets {
		var percent string
		switch {
		case b.GetPercentUsed().GetUnknown() != nil:
			percent = "unknown:" + b.GetPercentUsed().GetUnknown().GetReason().String()
		case b.GetPercentUsed() != nil:
			percent = "value:" + strconv.FormatUint(uint64(b.GetPercentUsed().GetValue()), 10)
		default:
			percent = "missing"
		}
		reset := "-"
		if rt := b.GetResetsTime(); rt != nil {
			reset = strconv.FormatInt(rt.GetResolvedTime().GetSeconds(), 10) + "|" + rt.GetOriginalText()
		}
		parts = append(parts, b.GetKey()+"|"+percent+"|"+reset)
	}
	return strings.Join(parts, "\n")
}
