package scrub_test

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// The hub pass (plan row 5l, 04 Q7 "Hub scrubber cost"). The hub scrubs the
// free text of messages the agent has already scrubbed, in the per-stream
// ingest goroutine. These corpora model what reaches it, at the 02e caps, and
// the benchmarks turn that into CPU cores for the hub carriers' real rates.
// Nothing here changes the scrubber.

const (
	// capMessage is the 02e cap of CommandResult.message,
	// CommandProgress.message and parse_message.
	capMessage = 1024
	// capShort is the 02e cap of ProbeHealth.message and
	// SettingRejection.message.
	capShort = 256
	// typicalSize is an assumed length of an ordinary error line before the
	// agent's pass (about 165 bytes after it). The real distribution is not
	// known until row 6 sends one; row 6 pastes it.
	typicalSize = 192

	agentHome = "/Users/al"
)

// fillTo repeats unit, cut to size bytes (ASCII units, so no split rune).
func fillTo(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit)+1)[:size]
}

// mixUnits are the sentences of a realistic failing command: five without a
// secret for each one with, so about two placeholders in 1 KiB once the
// agent has scrubbed it. The secret comes second so a 256-byte cut holds it.
var mixUnits = []string{
	"step 3 of 5 failed: go test ./... exited with status 1 in /Users/al/repos/fleet-board. ",
	"push to https://github.com/o/r?ref=main failed for al@example.com token ghp_" + "0123456789abcdefghijABCDEFGHIJ012345. ",
	"the worktree at /Users/al/repos/fleet-board-worktrees/r5l has no upstream branch yet. ",
	"retrying after 2s: the remote closed the connection before the push finished. ",
	"see the log for the failing package; nothing was changed on the remote. ",
	"open /Users/al/repos/x/go.mod: no such file or directory (exit status 128). ",
}

// agentScrubbed is raw text after the agent's pass: home written as ~,
// secrets as placeholders, cut to size. This is what the hub pass receives.
func agentScrubbed(raw string, size int) string {
	return scrub.Text(raw, scrub.Options{MaxBytes: size, Home: agentHome})
}

// mixRaw is the unscrubbed realistic text, at least size bytes long.
func mixRaw(size int) string {
	return fillTo(strings.Join(mixUnits, ""), size)
}

// secretUnits are shaped like the secrets the rules know, one of each kind in
// a row, so unscrubbed text of them needs the second round to confirm.
var secretUnits = strings.Join([]string{
	"GITHUB_TOKEN=ghp_" + "0123456789abcdefghijABCDEFGHIJ012345 ",
	"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789 ",
	"key sk-" + "ant-api03-abcdefghijklmnopqrstuvwxyz0123456789 ",
	"https://al:hunter2@db.example.com/x?code=abc123&state=xyz ",
	"mail al@example.com about AKIA" + "ABCDEFGHIJKLMNOP ",
	"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrst ",
	"db_password: hunter2 curl -u al:hunter2 https://x/ ",
}, "")

// hostileMessage is 1 KiB that uses every round and fails closed: four glued
// keys of alternating kinds take one round each, then the cheapest long run
// to scan (repeated --n, the slowest text per round found so far).
func hostileMessage(size int) string {
	aws := "AKIA" + strings.Repeat("Z", 16)
	google := "AIza" + strings.Repeat("x", 35)
	keys := aws + google + aws + google + " "
	return keys + fillTo("--n", size-len(keys))
}

type corpus struct {
	name string
	size int    // the field's cap
	text string // what the hub pass receives
}

// hubCorpora are the messages the hub pass is measured on.
//   - typical: an ordinary error line, already scrubbed by the agent.
//   - mix: a full-cap message, mostly benign with a few placeholders, already
//     scrubbed. The realistic upper size.
//   - unscrubbed: the mix before any agent scrubbed it (a buggy or old agent,
//     or text the agent did not know it must scrub): the first pass does all
//     the work, in two rounds. The costliest realistic text found.
//   - dense: a full-cap message that is nothing but secrets. Cheaper than the
//     mix, because a match is consumed in one step and the text shrinks.
//   - hostile: a full-cap message built to use every round.
func hubCorpora(size int) []corpus {
	return []corpus{
		{"typical", size, agentScrubbed(mixRaw(typicalSize), min(typicalSize, size))},
		{"mix", size, agentScrubbed(mixRaw(2*size), size)},
		{"unscrubbed", size, mixRaw(size)},
		{"dense", size, fillTo(secretUnits, size)},
		{"hostile", size, hostileMessage(size)},
	}
}

func hubPass(text string, size int) scrub.Result {
	return scrub.Scrub(text, scrub.Options{MaxBytes: size})
}

// The corpora must keep the premise the numbers rest on; if a rule change
// breaks one, the benchmark would measure something else.
func TestHubCorporaKeepTheirPremise(t *testing.T) {
	for _, size := range []int{capShort, capMessage} {
		for _, c := range hubCorpora(size) {
			t.Run(sizeName(size)+"/"+c.name, func(t *testing.T) {
				if len(c.text) == 0 || len(c.text) > size {
					t.Fatalf("size %d, want 1 to %d", len(c.text), size)
				}
				// The cost depends on length, so each corpus keeps its length.
				switch want := map[string]int{"typical": min(typicalSize, size), "unscrubbed": size, "dense": size, "hostile": size}[c.name]; {
				case c.name != "mix" && len(c.text) < want-want/4:
					t.Errorf("%s is %d bytes, want near %d", c.name, len(c.text), want)
				case c.name == "typical" && len(c.text) > want:
					t.Errorf("typical is %d bytes, want at most %d", len(c.text), want)
				}
				r := hubPass(c.text, size)
				switch c.name {
				case "typical", "mix":
					// Already scrubbed: the hub pass changes nothing.
					if r.Changed || r.Cut || r.Unsettled || r.Text != c.text {
						t.Errorf("hub pass changed agent-scrubbed text: %+v", r)
					}
					if c.name == "mix" && (len(c.text) < size-size/8 || !strings.Contains(c.text, "[")) {
						t.Errorf("mix is %d bytes with no placeholder, want near %d with one", len(c.text), size)
					}
					if strings.Contains(c.text, agentHome) {
						t.Errorf("home folder left in agent-scrubbed text")
					}
				case "unscrubbed", "dense":
					if !r.Changed || r.Unsettled {
						t.Errorf("%s corpus: %+v, want changed and settled", c.name, r)
					}
					if strings.Contains(r.Text, "hunter2") || strings.Contains(r.Text, "ghp_0123") {
						t.Errorf("secret left in %q", r.Text)
					}
				case "hostile":
					if !r.Unsettled {
						t.Errorf("hostile corpus settled; it no longer uses every round")
					}
				}
			})
		}
	}
}

// BenchmarkHubPass is the hub's cost for one message, per cap and corpus.
func BenchmarkHubPass(b *testing.B) {
	for _, size := range []int{capShort, capMessage} {
		for _, c := range hubCorpora(size) {
			b.Run(sizeName(size)+"/"+c.name, func(b *testing.B) {
				b.SetBytes(int64(len(c.text)))
				for b.Loop() {
					hubPass(c.text, size)
				}
			})
		}
	}
}

func sizeName(size int) string {
	if size == capMessage {
		return "1KiB"
	}
	return "256B"
}

// loadScenarios are the hub's message loads. msgs messages arrive in window,
// so the hub pass needs (time to scrub msgs) / window cores. Rates:
//   - commands 0.05 / s steady and 0.5 / s peak (03f, commands row), with an
//     assumed 5 CommandProgress messages per command (03f says "+ progress
//     steps" and gives no number); ProbeHealth and SettingRejection are
//     about 0 (03f, probes row) and are left out.
//   - the replay burst of 04 Q7: 2,500 events / s per hub. By 02i only
//     coalesced probe and usage levels carry free text in an event, so the
//     fraction is far below 1 %; 1 %, 10 % and 100 % bound it.
var loadScenarios = []struct {
	name   string
	corpus string
	msgs   int
	window time.Duration
}{
	{"steady/typical", "typical", 3, 10 * time.Second},
	{"peak/typical", "typical", 30, 10 * time.Second},
	{"peak/mix", "mix", 30, 10 * time.Second},
	{"peak/unscrubbed", "unscrubbed", 30, 10 * time.Second},
	{"peak/hostile", "hostile", 30, 10 * time.Second},
	{"burst_1pct/mix", "mix", 25, time.Second},
	{"burst_10pct/mix", "mix", 250, time.Second},
	{"burst_all/mix", "mix", 2500, time.Second},
	{"burst_all/unscrubbed", "unscrubbed", 2500, time.Second},
	{"burst_all/dense", "dense", 2500, time.Second},
}

// BenchmarkHubLoad runs one window of a scenario per iteration and reports
// the CPU cores one ingest goroutine would spend: ns/op / window. One stream
// scrubs on one goroutine, so this is also the load on that stream.
func BenchmarkHubLoad(b *testing.B) {
	byName := map[string]corpus{}
	for _, c := range hubCorpora(capMessage) {
		byName[c.name] = c
	}
	for _, s := range loadScenarios {
		c := byName[s.corpus]
		b.Run(s.name, func(b *testing.B) {
			for range b.N {
				for range s.msgs {
					hubPass(c.text, c.size)
				}
			}
			b.ReportMetric(float64(b.Elapsed())/float64(b.N)/float64(s.window), "cores")
		})
	}
}
