package opencode

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// commandCases is every expectation for Classify, worked out from the 01b
// class table by hand: the table's own examples keep their class, and every
// other name in the recorded catalogs is deny, since a discovered command
// whose effect is unprobed is never guessed into a sendable class. OpenCode
// has no /usage and no /goal (01b), so both are deny here. The never-send
// list (/login, /logout, /permissions, /config, /hooks, /plugin(s),
// /sandbox, usage-buying) is 01b's deny row and holds on every harness.
var commandCases = []struct {
	name    string
	command string
	want    typev1.CommandClass
}{
	{name: "status shows status, observe", command: "/status", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "diff opens the diff viewer, observe", command: "/diff", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "review reviews changes, review", command: "/review", want: typev1.CommandClass_COMMAND_CLASS_REVIEW},
	{name: "exit ends the app, destructive", command: "/exit", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "agents switches agent, effect unprobed", command: "/agents", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "connect adds a provider, credential-adjacent", command: "/connect", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "debug effect unprobed", command: "/debug", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "editor effect unprobed", command: "/editor", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "help effect unprobed", command: "/help", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "init writes setup files, deny", command: "/init", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "mcps toggles config, deny", command: "/mcps", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "models switches model, unprobed and not the table's model", command: "/models", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "move changes directory, effect unprobed", command: "/move", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "new starts a session, effect unprobed", command: "/new", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "sessions switches session, effect unprobed", command: "/sessions", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "skills effect unprobed", command: "/skills", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "themes switches theme, effect unprobed", command: "/themes", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "update upgrades the harness, never sent", command: "/update", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "variants switches model variant, effect unprobed", command: "/variants", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "goal has no native command on OpenCode", command: "/goal", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "usage has no command on OpenCode", command: "/usage", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "model singular is not the shown name", command: "/model", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "clear is not an OpenCode command", command: "/clear", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "quit is not an OpenCode command", command: "/quit", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "login is never sent", command: "/login", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "config is never sent", command: "/config", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "sandbox is never sent", command: "/sandbox", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "installed pack command is unknown, deny", command: "/gsd/plan", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "unknown command is deny", command: "/frobnicate", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "empty command is deny", command: "", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "missing slash is deny", command: "status", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "case is exact, uppercase is deny", command: "/STATUS", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "trailing space is not the shown name", command: "/exit ", want: typev1.CommandClass_COMMAND_CLASS_DENY},
}

func TestClassify(t *testing.T) {
	for _, tc := range commandCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Classify(tc.command); got != tc.want {
				t.Fatalf("Classify(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func readCatalog(t *testing.T, path string) string {
	t.Helper()
	//nolint:gosec // G304: a fixture under the package's own testdata, found by glob, test code only
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// TestClassifyCoversCatalog fails when any recorded versioned catalog holds
// a command with no expectation above: the C4 classification must cover
// every discovered command, and a new command in a later version's catalog
// must get a reviewed verdict here instead of silently falling into deny.
// It walks every versioned catalog fixture, so a new version directory is
// checked as soon as its catalog lands. Only the name before the first tab
// is checked, never the description tail (a 1.19.0 banner overwrites it).
func TestClassifyCoversCatalog(t *testing.T) {
	t.Parallel()
	covered := make(map[string]bool, len(commandCases))
	for _, tc := range commandCases {
		covered[tc.command] = true
	}
	catalogs, err := filepath.Glob(filepath.Join("testdata", "*", "tui-slash-menu-catalog.tsv"))
	if err != nil {
		t.Fatalf("glob catalogs: %v", err)
	}
	if len(catalogs) == 0 {
		t.Fatal("no versioned command catalog found under testdata/")
	}
	var missing []string
	for _, catalog := range catalogs {
		for line := range strings.Lines(strings.TrimSuffix(readCatalog(t, catalog), "\n")) {
			name, _, _ := strings.Cut(line, "\t")
			if name == "" {
				continue
			}
			if bare, _, _ := strings.Cut(name, " "); !covered[bare] {
				missing = append(missing, catalog+": "+name)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("catalog commands with no Classify expectation: %q", missing)
	}
}

// TestCommandNamesReviewed pins the F8 catalog list to the reviewed
// expectations: sorted, every name sendable, and every sendable expectation
// named. A table edit without a reviewed expectation fails here.
func TestCommandNamesReviewed(t *testing.T) {
	t.Parallel()
	names := CommandNames()
	if !slices.IsSorted(names) {
		t.Fatalf("CommandNames = %q, want sorted", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("CommandNames repeats %q", n)
		}
		seen[n] = true
		if got := Classify(n); got == typev1.CommandClass_COMMAND_CLASS_DENY {
			t.Fatalf("CommandNames lists %q, which classifies DENY", n)
		}
	}
	for _, tc := range commandCases {
		if tc.want == typev1.CommandClass_COMMAND_CLASS_DENY {
			continue
		}
		if !seen[tc.command] {
			t.Fatalf("sendable %q from the reviewed expectations is missing from CommandNames", tc.command)
		}
	}
}
