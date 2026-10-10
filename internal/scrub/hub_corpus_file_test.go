package scrub_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// corpusEnv names a file of real messages for BenchmarkHubPassFile: one JSON
// string per line, as they arrived from the agents (already scrubbed). Row 6
// records the CommandResult.message values of a real run this way, so the hub
// scrub cost is measured on real sizes and text, not on the model.
const corpusEnv = "FLEET_SCRUB_BENCH_CORPUS"

// loadCorpusFile reads the messages of a corpus file; blank lines are skipped.
func loadCorpusFile(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304, G703: a developer-supplied benchmark input file, test code only
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read only
	var msgs []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 2<<20) // a wire message is at most 1 MiB; JSON escapes may double it
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var m string
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return nil, fmt.Errorf("%s:%d: want one JSON string per line: %w", path, line, err)
		}
		msgs = append(msgs, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errors.New(path + ": no messages")
	}
	return msgs, nil
}

// BenchmarkHubPassFile is the hub pass over the messages of $FLEET_SCRUB_BENCH_CORPUS,
// one message per op, cycling. Metrics: the mean size and the cores one
// goroutine would spend at the peak rate (7.5 msg/s: 0.5 commands/s of 14
// StartGoal steps and a result) and at the replay burst (2,500 msg/s).
func BenchmarkHubPassFile(b *testing.B) {
	path := os.Getenv(corpusEnv)
	if path == "" {
		b.Skip(corpusEnv + " is not set")
	}
	msgs, err := loadCorpusFile(path)
	if err != nil {
		b.Fatal(err)
	}
	total := 0
	for _, m := range msgs {
		total += len(m)
	}
	b.ResetTimer()
	for i := range b.N {
		hubPass(msgs[i%len(msgs)], capMessage)
	}
	perMsg := float64(b.Elapsed()) / float64(b.N) / 1e9
	b.ReportMetric(float64(total)/float64(len(msgs)), "B/msg")
	b.ReportMetric(perMsg*peakMsgPerSec, "cores@peak")
	b.ReportMetric(perMsg*burstMsgPerSec, "cores@burst")
}

func TestLoadCorpusFile(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "corpus.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("one JSON string per line", func(t *testing.T) {
		got, err := loadCorpusFile(write(t, "\"exit status 1\"\n\n\"two\\nlines\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"exit status 1", "two\nlines"}; !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	for name, body := range map[string]string{
		"not a JSON string": "exit status 1\n",
		"a number":          "12\n",
		"empty":             "\n\n",
		"one good, one bad": "\"ok\"\nnot json\n",
		"a line over 2 MiB": "\"ok\"\n\"" + strings.Repeat("x", 2<<20) + "\"\n",
	} {
		t.Run("error: "+name, func(t *testing.T) {
			if got, err := loadCorpusFile(write(t, body)); err == nil {
				t.Errorf("got %q, want an error", got)
			}
		})
	}
	t.Run("error: no file", func(t *testing.T) {
		if _, err := loadCorpusFile(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Error("want an error")
		}
	})
}
