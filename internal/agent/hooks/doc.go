// Package hooks installs and verifies the agent-side goal hooks (G1,
// docs/specs/01d-goal-hooks.md): the user-level Claude Code settings
// entries, Codex hooks.json, the Muse plugin package and the Cursor
// hooks.json entries, all generated from one shared definition, plus the
// hook scripts that live under the agent data directory. The agent writes
// only its own named entries (commands pointing into the fleet hooks
// directory), backs the user file up first, and never approves hooks.
package hooks
