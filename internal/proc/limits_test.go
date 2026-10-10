package proc_test

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

const defaultsRowPrefix = "| One-shot child process (`internal/proc`) |"

// entry is `name` <seconds> / <KiB>: a caller whose caps were measured and
// written into the row.
var entry = regexp.MustCompile("`([a-z]+)` ([0-9]+) / ([0-9]+)")

// checkRow compares the caps written in a defaults row with a caller table and
// returns one message per difference. A row with no numbers and an empty table
// agree: no caller has been measured yet.
func checkRow(row string, table map[string]proc.Limits) []string {
	var problems []string
	docs := map[string]proc.Limits{}
	for _, m := range entry.FindAllStringSubmatch(row, -1) {
		secs, _ := strconv.Atoi(m[2])
		kib, _ := strconv.Atoi(m[3])
		if _, dup := docs[m[1]]; dup {
			problems = append(problems, fmt.Sprintf("the row names %q twice", m[1]))
		}
		docs[m[1]] = proc.Limits{Time: time.Duration(secs) * time.Second, Output: int64(kib) << 10}
	}
	for name, got := range table {
		want, ok := docs[name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("caller %q is in code and has no caps in the defaults row", name))
		case got != want:
			problems = append(problems, fmt.Sprintf("caller %q: code has %+v, the row says %+v", name, got, want))
		}
		if got.Time <= 0 || got.Output <= 0 {
			problems = append(problems, fmt.Sprintf("caller %q has a zero cap: %+v", name, got))
		}
	}
	for name := range docs {
		if _, ok := table[name]; !ok {
			problems = append(problems, fmt.Sprintf("the row has caps for %q and code has no such caller", name))
		}
	}
	slices.Sort(problems)
	return problems
}

func TestCheckRow(t *testing.T) {
	t.Parallel()
	const none = "| One-shot child process (`internal/proc`) | per named caller (`git`, `gh`), set from a measured run | s / KiB |"
	const git = "| One-shot child process (`internal/proc`) | `git` 120 / 1024 | s / KiB |"
	l := proc.Limits{Time: 120 * time.Second, Output: 1 << 20}
	tests := []struct {
		name  string
		row   string
		table map[string]proc.Limits
		want  int // problems
	}{
		{"no caller, no numbers", none, nil, 0},
		{"a caller with caps in the row", git, map[string]proc.Limits{"git": l}, 0},
		{"a caller the row has no caps for", none, map[string]proc.Limits{"git": l}, 1},
		{"a different number", git, map[string]proc.Limits{"git": {Time: 60 * time.Second, Output: 1 << 20}}, 1},
		{"caps in the row for a caller code lacks", git, nil, 1},
		{"a zero cap in code", "| One-shot child process (`internal/proc`) | `git` 0 / 0 |", map[string]proc.Limits{"git": {}}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := checkRow(tc.row, tc.table); len(got) != tc.want {
				t.Fatalf("problems = %q, want %d", got, tc.want)
			}
		})
	}
}

// TestCallersMatchTheDefaultsRow keeps the table in limits.go and the row
// "One-shot child process" of docs/specs/defaults.md in step. Both are empty
// of numbers today: each cap is set from a measured run of the real call, and
// no caller exists yet. A caller with no entry gets no default.
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
	for _, p := range checkRow(row, proc.Table()) {
		t.Error(p)
	}
	for name := range proc.Table() {
		if _, err := proc.For(name); err != nil {
			t.Errorf("For(%q): %v", name, err)
		}
	}
}

// lookup is what For does with the table: a found caller carries its caps, a
// name with no entry gets no default.
func TestLookupOverASeededTable(t *testing.T) {
	t.Parallel()
	table := map[string]proc.Limits{
		"git": {Time: 90 * time.Second, Output: 64 << 10},
		"gh":  {Time: 30 * time.Second, Output: 8 << 10},
	}
	for name, want := range table {
		c, err := proc.LookupForTest(table, name)
		if err != nil || c.Name() != name || c.Limits() != want {
			t.Errorf("lookup(%q) = %q %+v, %v; want its own caps %+v", name, c.Name(), c.Limits(), err, want)
		}
	}
	for _, name := range []string{"loginctl", "", "GIT"} {
		if c, err := proc.LookupForTest(table, name); !errors.Is(err, proc.ErrUnknownCaller) || c.Limits() != (proc.Limits{}) {
			t.Errorf("lookup(%q) = %+v, %v; want ErrUnknownCaller and no caps", name, c.Limits(), err)
		}
	}
	if _, err := proc.LookupForTest(nil, "git"); !errors.Is(err, proc.ErrUnknownCaller) {
		t.Errorf("lookup in an empty table = %v, want ErrUnknownCaller", err)
	}
}

// waitDelayInRow is "at most 2 s (`WaitDelay`)" in the defaults row.
var waitDelayInRow = regexp.MustCompile("at most ([0-9]+) s \\(`WaitDelay`\\)")

// The WaitDelay the row states is the constant in code.
func TestDefaultWaitDelayMatchesTheDefaultsRow(t *testing.T) {
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
	m := waitDelayInRow.FindStringSubmatch(row)
	if m == nil {
		t.Fatalf("the defaults row does not say \"at most N s (`WaitDelay`)\": %q", row)
	}
	secs, _ := strconv.Atoi(m[1])
	if got := time.Duration(secs) * time.Second; got != proc.DefaultWaitDelay {
		t.Fatalf("the row says %v, proc.DefaultWaitDelay is %v", got, proc.DefaultWaitDelay)
	}
}
