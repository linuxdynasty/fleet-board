package scrub_test

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// reportCap is the largest field the scrubber serves: the insights report
// (05, "Verification").
const reportCap = 64 << 10

// worstUnits, repeated, chain secrets with word characters, so an attempt
// that may start inside the run could start again after each match. With
// unbounded name runs in the rules the cost was quadratic in the length.
var worstUnits = []struct{ name, unit string }{
	{"chained jwts", "eyJaaaaaaaaa."},
	{"chained sk- keys", "abc.sk-aaaaaaaaaaaaaaaaaaaa."},
	{"chained aws key ids", "abc+def.AKIAAAAAAAAAAAAAAAAA."},
	{"chained emails", "a.a@b.co."},
	{"chained secret names", "x.password=a."},
	{"dash-joined key ids", "x-AKIAAAAAAAAAAAAAAAAA-"},
	{"chained url user names holding @", "x://a@b.co."},
	{"chained email passwords", "a@b.co:pw@h."},
	{"chained fragment values", "#a=b"},
}

func repeatTo(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit))
}

// bestOf is the fastest of n runs of Text on text, the least noisy reading.
func bestOf(n int, text string) time.Duration {
	opt := scrub.Options{MaxBytes: reportCap, Home: "/Users/al"}
	best := time.Duration(1<<63 - 1)
	for range n {
		start := time.Now()
		scrub.Text(text, opt)
		best = min(best, time.Since(start))
	}
	return best
}

// Four times the input should take about four times as long; a quadratic
// cost takes sixteen. The ratio holds on a slow or loaded machine where a
// wall-time budget would not. Not parallel: the inputs are timed one at a
// time. make test runs it in its pass without -race.
func TestTextWorstCaseStaysLinear(t *testing.T) {
	if raceDetector {
		t.Skip("timing ratio; runs without -race")
	}
	const maxRatio = 8
	for _, in := range worstUnits {
		t.Run(in.name, func(t *testing.T) {
			small := bestOf(5, repeatTo(in.unit, reportCap/4))
			large := bestOf(5, repeatTo(in.unit, reportCap))
			ratio := float64(large) / float64(max(small, time.Microsecond))
			t.Logf("16 KiB %v, 64 KiB %v, ratio %.1f", small, large, ratio)
			if ratio > maxRatio {
				t.Errorf("64 KiB took %.1f times as long as 16 KiB (%v, %v), want at most %d", ratio, large, small, maxRatio)
			}
		})
	}
}

func BenchmarkTextWorst(b *testing.B) {
	for _, in := range worstUnits {
		b.Run(in.name, func(b *testing.B) {
			text := repeatTo(in.unit, reportCap)
			opt := scrub.Options{MaxBytes: reportCap, Home: "/Users/al"}
			b.SetBytes(int64(len(text)))
			for b.Loop() {
				scrub.Text(text, opt)
			}
		})
	}
}

// BenchmarkTextMiB is 1 MiB with a 1 KiB cap: what a caller pays when it
// does not bound the input first (README, "Callers bound the input").
func BenchmarkTextMiB(b *testing.B) {
	for _, in := range []struct{ name, line string }{
		{"plain", "the build finished and the tests ran on every machine in the fleet. "},
		{"secrets", "open /Users/al/repos/x: push to https://github.com/o/r?ref=main failed for al@example.com; "},
	} {
		b.Run(in.name, func(b *testing.B) {
			text := repeatTo(in.line, 1<<20)
			opt := scrub.Options{MaxBytes: 1024, Home: "/Users/al"}
			b.SetBytes(int64(len(text)))
			for b.Loop() {
				scrub.Text(text, opt)
			}
		})
	}
}

// BenchmarkTextMessage is the common case: a 1 KiB command message.
func BenchmarkTextMessage(b *testing.B) {
	line := "open /Users/al/repos/x: push to https://github.com/o/r?ref=main failed for al@example.com; "
	msg := strings.Repeat(line, 1024/len(line)+1)[:1024]
	opt := scrub.Options{MaxBytes: 1024, Home: "/Users/al"}
	b.SetBytes(int64(len(msg)))
	for b.Loop() {
		scrub.Text(msg, opt)
	}
}

// BenchmarkTextScrubbed is the hub's pass over text the agent already
// scrubbed: it settles in the first round, so it costs one round.
func BenchmarkTextScrubbed(b *testing.B) {
	line := "open /Users/al/repos/x: push to https://github.com/o/r?ref=main failed for al@example.com; "
	opt := scrub.Options{MaxBytes: 1024, Home: "/Users/al"}
	msg := scrub.Text(strings.Repeat(line, 1024/len(line)+1)[:1024], opt)
	b.SetBytes(int64(len(msg)))
	for b.Loop() {
		scrub.Text(msg, opt)
	}
}
