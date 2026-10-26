package hookreconcile

// HookVersion is the hook script version the hub expects the agent to
// have installed. It duplicates internal/agent/hooks.HookVersion: the hub
// cannot import the agent's hook renderer (layering runs agent -> hub,
// never back), so versions_test.go pins the two together and fails the
// build on skew.
const HookVersion = "1"

// hookHarnesses are the harnesses with a defined hook shape (01d): the
// ones the "behind" gate can judge. opencode takes no hook install, so it
// stays out even though KnownHarnesses lists it for the button fan-out.
var hookHarnesses = []string{"claude", "codex", "muse", "cursor"}

// ExpectedVersions maps every hook-shaped harness to the version the hub
// expects: the Applier and the EnsureMachineHooks RPC gate "behind" on
// it. Empty means no gate (install and verify only).
func ExpectedVersions() map[string]string {
	out := make(map[string]string, len(hookHarnesses))
	for _, h := range hookHarnesses {
		out[h] = HookVersion
	}
	return out
}
