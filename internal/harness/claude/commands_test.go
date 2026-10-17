package claude

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// commandCases is every expectation for Classify, worked out from the 01b
// class table by hand: the table's own examples keep their class, and every
// other name in the recorded 2.1.296 catalog is deny, since a discovered
// command whose effect is unprobed is never guessed into a sendable class.
// The never-send list (/login, /logout, /permissions, /config, /hooks,
// /plugin(s), /sandbox, usage-buying) is 01b's deny row and holds on every
// harness.
var commandCases = []struct {
	name    string
	command string
	want    typev1.CommandClass
}{
	{name: "status shows status, observe", command: "/status", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "usage shows usage, observe", command: "/usage", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "context shows context, observe", command: "/context", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "diff shows changes, observe", command: "/diff", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "recap summarizes, observe", command: "/recap", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "tasks lists background work, observe", command: "/tasks", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "compact frees context, session", command: "/compact", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "model sets the model, session", command: "/model", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "effort sets effort, session", command: "/effort", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "fast toggles fast mode, session", command: "/fast", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "plan enters plan mode, session", command: "/plan", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "rename renames the chat, session", command: "/rename", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "resume resumes a chat, session", command: "/resume", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "fork copies the chat, session", command: "/fork", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "goal sets the durable goal, goal", command: "/goal", want: typev1.CommandClass_COMMAND_CLASS_GOAL},
	{name: "code-review reviews changes, review", command: "/code-review", want: typev1.CommandClass_COMMAND_CLASS_REVIEW},
	{name: "security-review reviews security, review", command: "/security-review", want: typev1.CommandClass_COMMAND_CLASS_REVIEW},
	{name: "clear starts over, destructive", command: "/clear", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "rewind restores a point, destructive", command: "/rewind", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "exit ends the session, destructive", command: "/exit", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "add-dir effect unprobed", command: "/add-dir", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "advisor effect unprobed", command: "/advisor", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "artifact-capabilities effect unprobed", command: "/artifact-capabilities", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "artifact-diagramming effect unprobed", command: "/artifact-diagramming", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "artifacts effect unprobed", command: "/artifacts", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "auto-mode-setup effect unprobed", command: "/auto-mode-setup", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "autocompact effect unprobed", command: "/autocompact", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "autofix-pr effect unprobed", command: "/autofix-pr", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "background effect unprobed", command: "/background", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "batch effect unprobed", command: "/batch", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "branch effect unprobed", command: "/branch", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "btw effect unprobed", command: "/btw", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "bug effect unprobed", command: "/bug", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "cd effect unprobed", command: "/cd", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "chrome effect unprobed", command: "/chrome", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "claude-api effect unprobed", command: "/claude-api", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "claude-in-chrome effect unprobed", command: "/claude-in-chrome", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "color effect unprobed", command: "/color", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "config is never sent", command: "/config", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "copy effect unprobed", command: "/copy", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "dataviz effect unprobed", command: "/dataviz", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "debug effect unprobed", command: "/debug", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "deep-research effect unprobed", command: "/deep-research", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "design effect unprobed", command: "/design", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "design-login effect unprobed", command: "/design-login", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "design-sync effect unprobed", command: "/design-sync", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "desktop effect unprobed", command: "/desktop", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "doctor effect unprobed", command: "/doctor", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "export effect unprobed", command: "/export", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "feedback effect unprobed", command: "/feedback", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "fewer-permission-prompts effect unprobed", command: "/fewer-permission-prompts", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "focus effect unprobed", command: "/focus", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "help effect unprobed", command: "/help", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "hooks is never sent", command: "/hooks", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "ide effect unprobed", command: "/ide", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "import effect unprobed", command: "/import", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "init effect unprobed", command: "/init", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "insights effect unprobed", command: "/insights", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "install-github-app is never sent", command: "/install-github-app", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "install-slack-app is never sent", command: "/install-slack-app", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "keybindings effect unprobed", command: "/keybindings", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "list-agents effect unprobed", command: "/list-agents", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "login is never sent", command: "/login", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "logout is never sent", command: "/logout", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "loop effect unprobed", command: "/loop", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "mcp effect unprobed", command: "/mcp", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "memory effect unprobed", command: "/memory", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "mobile effect unprobed", command: "/mobile", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "output-style effect unprobed", command: "/output-style", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "passes effect unprobed", command: "/passes", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "permissions is never sent", command: "/permissions", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "plugin is never sent", command: "/plugin", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "plugin-authoring effect unprobed", command: "/plugin-authoring", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "powerup effect unprobed", command: "/powerup", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "privacy-settings is never sent", command: "/privacy-settings", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "radio effect unprobed", command: "/radio", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "rate-limit-options is never sent", command: "/rate-limit-options", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "release-notes effect unprobed", command: "/release-notes", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "reload-plugins effect unprobed", command: "/reload-plugins", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "reload-skills effect unprobed", command: "/reload-skills", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "remote-control is never sent", command: "/remote-control", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "remote-env effect unprobed", command: "/remote-env", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "run effect unprobed", command: "/run", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "run-skill-generator effect unprobed", command: "/run-skill-generator", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "sandbox is never sent", command: "/sandbox", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "schedule effect unprobed", command: "/schedule", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "simplify effect unprobed", command: "/simplify", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "skill-doctor effect unprobed", command: "/skill-doctor", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "skills effect unprobed", command: "/skills", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "slides effect unprobed", command: "/slides", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "statusline effect unprobed", command: "/statusline", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "stickers effect unprobed", command: "/stickers", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "subtask effect unprobed", command: "/subtask", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "team-onboarding effect unprobed", command: "/team-onboarding", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "teleport effect unprobed", command: "/teleport", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "terminal-setup effect unprobed", command: "/terminal-setup", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "theme effect unprobed", command: "/theme", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "tui effect unprobed", command: "/tui", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "ultrareview effect unprobed", command: "/ultrareview", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "update-config effect unprobed", command: "/update-config", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "upgrade is never sent", command: "/upgrade", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "usage buying is never sent", command: "/usage-credits", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "verify effect unprobed", command: "/verify", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "voice effect unprobed", command: "/voice", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "web-setup effect unprobed", command: "/web-setup", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "workflow-authoring effect unprobed", command: "/workflow-authoring", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "workflows effect unprobed", command: "/workflows", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "ps is observe on the shared table", command: "/ps", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "hud is observe on the shared table", command: "/hud", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
	{name: "stop is session on the shared table", command: "/stop", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
	{name: "review is review on the shared table", command: "/review", want: typev1.CommandClass_COMMAND_CLASS_REVIEW},
	{name: "delete is destructive on the shared table", command: "/delete", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "archive is destructive on the shared table", command: "/archive", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "quit is destructive on the shared table", command: "/quit", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
	{name: "settings is never sent", command: "/settings", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	{name: "plugins is never sent", command: "/plugins", want: typev1.CommandClass_COMMAND_CLASS_DENY},
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

// TestClassifyCoversCatalog fails when the recorded 2.1.296 catalog holds a
// command with no expectation above: the C4 classification must cover every
// discovered command, and a new command in a later catalog must get a reviewed
// verdict here instead of silently falling into deny.
func TestClassifyCoversCatalog(t *testing.T) {
	t.Parallel()
	covered := make(map[string]bool, len(commandCases))
	for _, tc := range commandCases {
		covered[tc.command] = true
	}
	var missing []string
	for line := range strings.Lines(strings.TrimSuffix(readData(t, "2.1.296/help-commands-catalog.tsv"), "\n")) {
		name, _, _ := strings.Cut(line, "\t")
		if name == "" {
			continue
		}
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("catalog commands with no Classify expectation: %q", missing)
	}
}
