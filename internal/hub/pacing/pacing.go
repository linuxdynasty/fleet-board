package pacing

import (
	"math"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Tuning from 03b and defaults.md. The scheduler passes them through [Input];
// the constants are the documented defaults.
const (
	// BaseCap is the concurrent-goal allowance per account every harness
	// shares (defaults.md).
	BaseCap = 2
	// DefaultStaleAfter is how old a reading may be before the account stops
	// dispatching: 3 probe intervals of 10 minutes (defaults.md, 03b).
	DefaultStaleAfter = 30 * time.Minute
	// ProjectionWait is how far into a usage window the projection stays
	// unset: a tiny slice of the week makes it jumpy (03b, ADR 0024).
	ProjectionWait = 24 * time.Hour
	// ShortLimitPercent is the short-bucket level that stops new dispatch
	// until its reset (03b step 4).
	ShortLimitPercent = 95
	// lastDayIgnore is the stretch before a reset where the burn-down line
	// is ignored and the base cap applies until the reserve (03b step 3).
	lastDayIgnore = 24 * time.Hour
	// maxReservePercent bounds reserve_percent (defaults.md: 0 to 50).
	maxReservePercent = 50
)

// Input is everything [Evaluate] needs for one account. Times the caller
// passes are hub-adjusted (02i, "Clocks"): read_time already has the
// machine's measured skew applied.
type Input struct {
	// ReservePercent is the quota kept for interactive use (0 to 50).
	ReservePercent uint32
	Budget         typev1.BudgetKind
	Mode           hubv1.AccountMode
	Overage        typev1.OverageBilling
	// GoalModel selects the per-model bucket the work needs; empty means no
	// per-model bucket is needed (03b step 2).
	GoalModel string
	// ActiveGoals is passed through to the verdict for display.
	ActiveGoals uint32
	// StaleAfter overrides DefaultStaleAfter when positive (tests).
	StaleAfter time.Duration
	// SeenWindow is the window the hub saw between the last two resets of
	// a monthly bucket, used only when HaveSeenWindow (03b rule 1). The
	// store slice validates the 28 d to 31 d 1 h span before setting it.
	SeenWindow     time.Duration
	HaveSeenWindow bool
	// UsageLimitUntil marks an open USAGE_LIMIT condition until its reset
	// (or the deciding bucket's); zero means none (03b step 7).
	UsageLimitUntil time.Time
	// Usage is the latest PROBE reading. Nil means no reading yet.
	Usage *hubv1.AccountUsage
}

// Result is the verdict plus the debug the pacing.decision log needs.
type Result struct {
	Pacing *hubv1.AccountPacing
	// Window is the source of the deciding bucket's window.
	Window WindowSource
	// Reason is a short stable cause ("ahead:+7", "reserve:weekly_all",
	// "unknown:window_unknown"). Bucket keys are adapter-defined stable
	// keys, never free text.
	Reason string
}

// Evaluate applies the 03b pacing decision to one account. Precedence:
// EXCLUDED, then UNKNOWN (no reading or parse failed), then STALE (reading
// older than stale_after), then RESERVE, then SHORT_LIMIT, then UNKNOWN for
// the line, then the burn-down bands. RESERVE beats SHORT_LIMIT so the
// reserve alert still fires when both hit; a concrete SHORT_LIMIT beats an
// unknown line. Rank and idle causes belong to the multi-account scheduler
// and stay unset here.
func Evaluate(now time.Time, in Input) Result {
	out := &hubv1.AccountPacing{ActiveGoals: in.ActiveGoals}
	res := Result{Pacing: out, Reason: "ok"}

	if cause := excludedCause(in); cause != "" {
		out.State = hubv1.PacingState_PACING_STATE_EXCLUDED
		res.Reason = "excluded:" + cause
		return res
	}
	u := in.Usage
	if u == nil {
		out.State = hubv1.PacingState_PACING_STATE_UNKNOWN
		res.Reason = "unknown:no_reading"
		return res
	}
	if u.GetParse() == typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
		// A failed parse makes the account UNKNOWN at once, even when a
		// good reading is a minute old (03b, "Readings").
		out.State = hubv1.PacingState_PACING_STATE_UNKNOWN
		res.Reason = "unknown:parse_failed"
		return res
	}

	staleAfter := in.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	readTime := timestampTime(u.GetReadTime())
	fresh := !now.After(readTime.Add(staleAfter))
	if !fresh {
		out.State = hubv1.PacingState_PACING_STATE_STALE
		if b, ok := decideLineBucket(now, u, in); ok {
			out.ReadingStale = predatesReset(readTime, b, in)
		}
		res.Reason = "stale:reading_age"
		return res
	}

	needed := neededBuckets(u.GetBuckets(), in.GoalModel)
	known := knownPercents(needed)

	if key, ok := reserveBucket(known, reserve(in.ReservePercent)); ok {
		out.State = hubv1.PacingState_PACING_STATE_RESERVE
		out.DecidingBucket = key
		res.Reason = "reserve:" + key
		if d, ok := lineBucket(now, needed, key, in); ok {
			fillProjection(now, out, d, 100-reserve(in.ReservePercent))
		}
		if b, ok := decideLineBucket(now, u, in); ok {
			out.ReadingStale = predatesReset(readTime, b, in)
		}
		return res
	}
	if key, ok := shortLimitBucket(u.GetBuckets()); ok {
		out.State = hubv1.PacingState_PACING_STATE_SHORT_LIMIT
		out.DecidingBucket = key
		res.Reason = "short_limit:" + key
		if b, ok := decideLineBucket(now, u, in); ok {
			out.ReadingStale = predatesReset(readTime, b, in)
		}
		return res
	}
	if !in.UsageLimitUntil.IsZero() && now.Before(in.UsageLimitUntil) {
		out.State = hubv1.PacingState_PACING_STATE_SHORT_LIMIT
		res.Reason = "short_limit:usage_condition"
		if d, ok := decideLine(now, needed, in); ok {
			out.DecidingBucket = d.b.GetKey()
			out.AheadPoints = int32(math.Round(d.ahead))
			res.Window = d.src
		}
		if b, ok := decideLineBucket(now, u, in); ok {
			out.ReadingStale = predatesReset(readTime, b, in)
		}
		return res
	}
	if key, ok := unknownBucket(needed, known); ok {
		// A row the screen should show and does not stays unknown; it is
		// never guessed (03b, "Monthly windows"). The reserve check above
		// already applied to every known percent.
		out.State = hubv1.PacingState_PACING_STATE_UNKNOWN
		res.Reason = "unknown:bucket_unknown:" + key
		return res
	}
	d, ok := decideLine(now, needed, in)
	if !ok {
		// No needed bucket has a usable window. The account is UNKNOWN for
		// the line, but the reserve check above already applied to every
		// known percent (03b, "Monthly windows").
		out.State = hubv1.PacingState_PACING_STATE_UNKNOWN
		res.Reason = "unknown:window_unknown"
		return res
	}
	out.DecidingBucket = d.b.GetKey()
	out.AheadPoints = int32(math.Round(d.ahead))
	res.Window = d.src

	if d.resets.Sub(now) < lastDayIgnore {
		// The last 24 h before the reset: the line is ignored and the base
		// cap applies until the reserve (03b step 3).
		out.State = hubv1.PacingState_PACING_STATE_OK
		out.AllowedActive = BaseCap
		res.Reason = "ok:last_day"
	} else {
		out.AllowedActive, out.State, res.Reason = band(d.ahead)
	}
	out.ReadingStale = predatesReset(readTime, d.b, in)
	fillProjection(now, out, d, 100-reserve(in.ReservePercent))
	return res
}

// band maps ahead points to the allowance and state (03b step 3). The bounds
// are strict: exactly +5 is OK, exactly +10 is AHEAD with one fewer worker.
func band(ahead float64) (uint32, hubv1.PacingState, string) {
	switch {
	case ahead > 10:
		return 0, hubv1.PacingState_PACING_STATE_AHEAD, "ahead:stop"
	case ahead > 5:
		return BaseCap - 1, hubv1.PacingState_PACING_STATE_AHEAD, "ahead:trim"
	case ahead < -5:
		return BaseCap + 1, hubv1.PacingState_PACING_STATE_BEHIND, "behind:add"
	default:
		return BaseCap, hubv1.PacingState_PACING_STATE_OK, "ok"
	}
}

// fillProjection sets projected_unused_percent when the inputs allow it: a
// known deciding window, a fresh reading, and at least 24 h elapsed. It is
// never guessed (03b, "Projection").
func fillProjection(now time.Time, out *hubv1.AccountPacing, d decided, limit float64) {
	elapsed := now.Sub(d.start)
	if elapsed < ProjectionWait {
		return
	}
	f := float64(elapsed) / float64(d.window)
	projected := limit - d.used/f
	if projected < 0 {
		projected = 0
	}
	v := int32(math.Round(projected))
	out.ProjectedUnusedPercent = &v
}

// excludedCause names why the account never auto-dispatches (03b step 6).
func excludedCause(in Input) string {
	switch {
	case in.Budget == typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP:
		return "dollar_cap"
	case in.Mode == hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY:
		return "interactive_only"
	case in.Overage == typev1.OverageBilling_OVERAGE_BILLING_ON:
		return "overage_billing_on"
	case in.Overage == typev1.OverageBilling_OVERAGE_BILLING_UNKNOWN:
		return "overage_billing_unknown"
	default:
		return ""
	}
}

// reserve clamps the reserve to its documented range (defaults.md: 0 to 50).
func reserve(p uint32) float64 {
	if p > maxReservePercent {
		p = maxReservePercent
	}
	return float64(p)
}

// neededBuckets returns the buckets the work needs: every weekly and monthly
// bucket, plus the per-model bucket of the goal's model (03b step 2). A
// Cursor goal needs all three monthly buckets, so the worst decides.
func neededBuckets(buckets []*typev1.LimitBucket, goalModel string) []*typev1.LimitBucket {
	var out []*typev1.LimitBucket
	for _, b := range buckets {
		switch b.GetKind() {
		case typev1.WindowKind_WINDOW_KIND_WEEKLY, typev1.WindowKind_WINDOW_KIND_MONTHLY:
			out = append(out, b)
		case typev1.WindowKind_WINDOW_KIND_PER_MODEL:
			if goalModel != "" && b.GetModel() == goalModel {
				out = append(out, b)
			}
		default:
			// Short buckets are a speed limit only, never the pace line.
		}
	}
	return out
}

// knownPercents keeps the buckets with a known percent. Anything else stays
// unknown and is never guessed.
func knownPercents(buckets []*typev1.LimitBucket) map[string]float64 {
	out := map[string]float64{}
	for _, b := range buckets {
		if v, ok := percentValue(b.GetPercentUsed()); ok {
			out[b.GetKey()] = v
		}
	}
	return out
}

// unknownBucket returns a needed bucket with no known percent, if any.
func unknownBucket(needed []*typev1.LimitBucket, known map[string]float64) (string, bool) {
	for _, b := range needed {
		if _, ok := known[b.GetKey()]; !ok {
			return b.GetKey(), true
		}
	}
	return "", false
}

// reserveBucket returns the highest known needed bucket at or above the
// reserve line.
func reserveBucket(known map[string]float64, limit float64) (string, bool) {
	best := ""
	bestUsed := 0.0
	for key, used := range known {
		if used >= 100-limit && (best == "" || used > bestUsed) {
			best, bestUsed = key, used
		}
	}
	return best, best != ""
}

// shortLimitBucket returns the highest short bucket at or above the speed
// limit. Short buckets are read from the full reading elsewhere; the caller
// passes only short buckets here.
func shortLimitBucket(buckets []*typev1.LimitBucket) (string, bool) {
	best := ""
	bestUsed := 0.0
	for _, b := range buckets {
		if b.GetKind() != typev1.WindowKind_WINDOW_KIND_SHORT {
			continue
		}
		if v, ok := percentValue(b.GetPercentUsed()); ok && v >= ShortLimitPercent && v > bestUsed {
			best, bestUsed = b.GetKey(), v
		}
	}
	return best, best != ""
}

// percentValue returns a bucket percent, or false when it is unknown or
// missing. Values above 100 are rejected by the hub on ingest; here they are
// capped so a bad reading cannot push the line past full.
func percentValue(p *typev1.Percent) (float64, bool) {
	v, ok := p.GetKind().(*typev1.Percent_Value)
	if !ok {
		return 0, false
	}
	if v.Value > 100 {
		return 100, true
	}
	return float64(v.Value), true
}

// timestampTime returns the instant, or zero when unset or invalid.
func timestampTime(t *timestamppb.Timestamp) time.Time {
	if t == nil || !t.IsValid() {
		return time.Time{}
	}
	return t.AsTime()
}

// predatesReset reports whether the reading predates the bucket window's
// last reset: a re-probe is pending (AccountPacing.reading_stale).
func predatesReset(read time.Time, b *typev1.LimitBucket, in Input) bool {
	if b == nil || read.IsZero() {
		return false
	}
	resets := timestampTime(b.GetResetsTime().GetResolvedTime())
	window, _, ok := resolveWindow(b, in)
	if resets.IsZero() || !ok {
		return false
	}
	return read.Before(resets.Add(-window))
}
