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

// worstInputs are 64 KiB of secrets chained by word characters, so an
// attempt that may start inside the run could start again after each match.
// With unbounded name runs in the rules each took 3 to 13 s; bounded, under
// 100 ms on the same machine.
var worstInputs = []struct{ name, text string }{
	{"chained jwts", strings.Repeat("eyJaaaaaaaaa.", reportCap/13)},
	{"chained sk- keys", strings.Repeat("abc.sk-aaaaaaaaaaaaaaaaaaaa.", reportCap/28)},
	{"chained aws key ids", strings.Repeat("abc+def.AKIAAAAAAAAAAAAAAAAA.", reportCap/29)},
	{"chained emails", strings.Repeat("a.a@b.co.", reportCap/9)},
}

// The budget is far above the linear time and far below the quadratic one.
// Not parallel: the inputs are timed one at a time. make test runs it in its
// pass without -race.
func TestTextWorstCaseStaysLinear(t *testing.T) {
	if raceDetector {
		t.Skip("wall-time budget; runs without -race")
	}
	const budget = 3 * time.Second
	for _, in := range worstInputs {
		t.Run(in.name, func(t *testing.T) {
			start := time.Now()
			scrub.Text(in.text, scrub.Options{MaxBytes: reportCap, Home: "/Users/al"})
			if took := time.Since(start); took > budget {
				t.Errorf("Text of %d bytes took %v, budget %v", len(in.text), took, budget)
			}
		})
	}
}

func BenchmarkTextWorst(b *testing.B) {
	for _, in := range worstInputs {
		b.Run(in.name, func(b *testing.B) {
			opt := scrub.Options{MaxBytes: reportCap, Home: "/Users/al"}
			b.SetBytes(int64(len(in.text)))
			for b.Loop() {
				scrub.Text(in.text, opt)
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
