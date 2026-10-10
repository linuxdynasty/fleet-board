package proc_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

const defaultsRowPrefix = "| One-shot child process (`internal/proc`) |"

// entry is `name` <seconds> / <KiB> in the row's value cell.
var entry = regexp.MustCompile("`([a-z]+)` ([0-9]+) / ([0-9]+)")

// TestCallersMatchTheDefaultsRow keeps the table in limits.go and the row
// "One-shot child process" of docs/specs/defaults.md in step: the same names,
// the same caps (seconds and KiB). A caller with no entry in the row gets no
// default.
func TestCallersMatchTheDefaultsRow(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../docs/specs/defaults.md")
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.HasPrefix(line, defaultsRowPrefix) {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("defaults.md has no row starting %q", defaultsRowPrefix)
	}
	docs := map[string]proc.Limits{}
	for _, m := range entry.FindAllStringSubmatch(row, -1) {
		secs, _ := strconv.Atoi(m[2])
		kib, _ := strconv.Atoi(m[3])
		if _, dup := docs[m[1]]; dup {
			t.Errorf("the row names %q twice", m[1])
		}
		docs[m[1]] = proc.Limits{Time: time.Duration(secs) * time.Second, Output: int64(kib) << 10}
	}
	if len(docs) == 0 {
		t.Fatalf("no `name` seconds / KiB entries in %q", row)
	}
	names := proc.Names()
	for _, name := range names {
		want, ok := docs[name]
		if !ok {
			t.Errorf("caller %q is in code and has no entry in the defaults row", name)
			continue
		}
		c, err := proc.For(name)
		if err != nil {
			t.Errorf("For(%q): %v", name, err)
			continue
		}
		if got := c.Limits(); got != want {
			t.Errorf("caller %q: code has %+v, the row says %+v", name, got, want)
		}
		if got := c.Limits(); got.Time <= 0 || got.Output <= 0 {
			t.Errorf("caller %q has a zero cap: %+v", name, got)
		}
	}
	for name := range docs {
		if _, err := proc.For(name); err != nil {
			t.Errorf("the row names %q and code has no such caller: %v", name, err)
		}
	}
	if len(names) != len(docs) {
		t.Errorf("code has %d callers, the row has %d", len(names), len(docs))
	}
}
