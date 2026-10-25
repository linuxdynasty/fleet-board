package cursor

import (
	"slices"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The C4 command catalog (01b) as classifier tests: every slash command the
// recorded probes saw, each in its 01b class, and deny by default. Every
// expectation below was worked out from the spec by hand: /usage, /model
// and /quit are 01b's own class examples; /goal starts the durable goal
// (01b2); the never-send list (/login, /logout, /permissions, /config,
// /hooks, /plugin(s), /sandbox, usage-buying) is 01b's deny row and holds
// on every harness. Commands the probes saw but whose effect is unprobed
// (/run-everything, /auto-review, /plan, /ask, /loop, /statusline,
// /status-indicators, /bedrock, /internal-comms) are deny: an unprobed
// effect is never guessed into a sendable class.
func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    typev1.CommandClass
	}{
		{name: "usage shows the plan, observe", command: "/usage", want: typev1.CommandClass_COMMAND_CLASS_OBSERVE},
		{name: "model picks the model, session", command: "/model", want: typev1.CommandClass_COMMAND_CLASS_SESSION},
		{name: "goal starts the durable goal, goal", command: "/goal", want: typev1.CommandClass_COMMAND_CLASS_GOAL},
		{name: "quit ends the session, destructive", command: "/quit", want: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE},
		{name: "login is never sent", command: "/login", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "logout is never sent", command: "/logout", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "permissions is never sent", command: "/permissions", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "config is never sent", command: "/config", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "hooks is never sent", command: "/hooks", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "plugin is never sent", command: "/plugin", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "plugins is never sent", command: "/plugins", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "sandbox is never sent", command: "/sandbox", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "upgrade is never sent", command: "/upgrade", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "usage buying is never sent", command: "/usage-credits", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "run-everything effect unprobed", command: "/run-everything", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "auto-review effect unprobed", command: "/auto-review", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "plan effect unprobed", command: "/plan", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "ask effect unprobed", command: "/ask", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "loop effect unprobed", command: "/loop", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "statusline effect unprobed", command: "/statusline", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "status-indicators effect unprobed", command: "/status-indicators", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "bedrock effect unprobed", command: "/bedrock", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "internal-comms effect unprobed", command: "/internal-comms", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "unknown command is deny", command: "/frobnicate", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "empty command is deny", command: "", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "missing slash is deny", command: "usage", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "case is exact, uppercase is deny", command: "/USAGE", want: typev1.CommandClass_COMMAND_CLASS_DENY},
		{name: "trailing space is not the shown name", command: "/goal ", want: typev1.CommandClass_COMMAND_CLASS_DENY},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Classify(tc.command); got != tc.want {
				t.Fatalf("Classify(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestCommandNamesReviewed pins the F8 catalog list to the reviewed 01b
// classes: sorted, every name sendable, exactly the four reviewed names.
// A table edit without a reviewed expectation fails here.
func TestCommandNamesReviewed(t *testing.T) {
	t.Parallel()
	names := CommandNames()
	if !slices.IsSorted(names) {
		t.Fatalf("CommandNames = %q, want sorted", names)
	}
	want := []string{"/goal", "/model", "/quit", "/usage"}
	if !slices.Equal(names, want) {
		t.Fatalf("CommandNames = %q, want %q", names, want)
	}
	for _, n := range names {
		if got := Classify(n); got == typev1.CommandClass_COMMAND_CLASS_DENY {
			t.Fatalf("CommandNames lists %q, which classifies DENY", n)
		}
	}
}
