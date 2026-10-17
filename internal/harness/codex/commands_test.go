package codex

import (
	"path/filepath"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// commandCases is every expectation for Classify, worked out from the 01b
// class table by hand: the table's own examples keep their class, and every
// other name in the recorded catalogs is deny, since a discovered
// command whose effect is unprobed is never guessed into a sendable class.
// The never-send list (/login, /logout, /permissions, /config, /hooks,
// /plugin(s), /sandbox, usage-buying) is 01b's deny row and holds on every
// harness.
var commandCases = []struct {
	name    string
	command string
	want    typev1.CommandClass
}{
	{name: "status reads the card, observe", command: "/status", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "usage menu, observe", command: "/usage", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "diff shows changes, observe", command: "/diff", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "recap summarizes, observe", command: "/recap", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "compact summarizes the chat, session", command: "/compact", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "model picks the model, session", command: "/model", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "fast sets speed, session", command: "/fast", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "plan switches mode, session", command: "/plan", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "rename renames the thread, session", command: "/rename", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "resume resumes a chat, session", command: "/resume", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "fork forks the chat, session", command: "/fork", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "goal sets the durable goal, goal", command: "/goal", want: typev1.CommandClass_COMMAND_CLASS_GOAL},
	{name: "review reviews changes, review", command: "/review", want: typev1.CommandClass_COMMAND_CLASS_REVIEW},
	{name: "archive archives the session, destructive", command: "/archive", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "delete deletes the session, destructive", command: "/delete", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "exit ends the session, destructive", command: "/exit", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "agents effect unprobed", command: "/agents", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "app effect unprobed", command: "/app", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "approve effect unprobed", command: "/approve", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "cd effect unprobed", command: "/cd", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "copy effect unprobed", command: "/copy", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "daemon effect unprobed", command: "/daemon", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "experimental effect unprobed", command: "/experimental", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "export effect unprobed", command: "/export", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "hooks is never sent", command: "/hooks", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "ide effect unprobed", command: "/ide", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "import effect unprobed", command: "/import", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "init effect unprobed", command: "/init", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "keymap effect unprobed", command: "/keymap", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "logout is never sent", command: "/logout", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "mcp effect unprobed", command: "/mcp", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "memories effect unprobed", command: "/memories", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "mention effect unprobed", command: "/mention", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "new starts a chat, effect unprobed", command: "/new", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "permissions is never sent", command: "/permissions", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "pets effect unprobed", command: "/pets", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "plugins is never sent", command: "/plugins", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "pwd effect unprobed", command: "/pwd", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "raw effect unprobed", command: "/raw", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "side forks a conversation, effect unprobed", command: "/side", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "skills effect unprobed", command: "/skills", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "statusline effect unprobed", command: "/statusline", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "theme effect unprobed", command: "/theme", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "title effect unprobed", command: "/title", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "tui effect unprobed", command: "/tui", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "vim effect unprobed", command: "/vim", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "voice effect unprobed", command: "/voice", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "warnings effect unprobed", command: "/warnings", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "worktree moves the chat, effect unprobed", command: "/worktree", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "feedback sends logs, effect unprobed", command: "/feedback", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "subagents switches subagents, effect unprobed", command: "/subagents", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "clear is destructive on the shared table", command: "/clear", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "quit is destructive on the shared table", command: "/quit", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "rewind is destructive on the shared table", command: "/rewind", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "stop is session on the shared table", command: "/stop", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "context is observe on the shared table", command: "/context", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "ps is observe on the shared table", command: "/ps", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "tasks is observe on the shared table", command: "/tasks", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "code-review is review on the shared table", command: "/code-review", want: typev1.CommandClass_COMMAND_CLASS_REVIEW},
	{name: "login is never sent", command: "/login", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "config is never sent", command: "/config", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "sandbox is never sent", command: "/sandbox", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "upgrade is never sent", command: "/upgrade", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "usage buying is never sent", command: "/usage-credits", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "unknown command is deny", command: "/frobnicate", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "empty command is deny", command: "", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "missing slash is deny", command: "usage", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "case is exact, uppercase is deny", command: "/USAGE", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "trailing space is not the shown name", command: "/goal ", want: typev1.CommandClass_COMMAND_CLASS_DENY},
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

// TestClassifyCoversCatalog fails when any recorded versioned catalog holds
// a command with no expectation above: the C4 classification must cover
// every discovered command, and a new command in a later version's catalog
// must get a reviewed verdict here instead of silently falling into deny.
// It walks every versioned catalog fixture, so a new version directory is
// checked as soon as its catalog lands.
func TestClassifyCoversCatalog(t *testing.T) {
	t.Parallel()
	covered := make(map[string]bool, len(commandCases))
	for _, tc := range commandCases {
		covered[tc.command] = true
	}
	catalogs, err := filepath.Glob(filepath.Join("testdata", "*", "slash-menu-catalog.tsv"))
	if err != nil {
		t.Fatalf("glob catalogs: %v", err)
	}
	if len(catalogs) == 0 {
		t.Fatal("no versioned command catalog found under testdata/")
	}
	var missing []string
	for _, catalog := range catalogs {
		for line := range strings.Lines(strings.TrimSuffix(readData(t, strings.TrimPrefix(catalog, "testdata/")), "\n")) {
			name, _, _ := strings.Cut(line, "\t")
			if name == "" {
				continue
			}
			if !covered[name] {
				missing = append(missing, catalog+": "+name)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("catalog commands with no Classify expectation: %q", missing)
	}
}
