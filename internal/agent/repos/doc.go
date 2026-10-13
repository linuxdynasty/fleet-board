// Package repos reports the machine's git checkouts and forge access
// (H1, H3, H4): every checkout under the allowed roots with its RepoRef,
// branch state and worktree shape, and each code-host CLI's sign-in with
// per-repo push access. The observed-goal tracker (internal/agent/goals)
// reuses the checkout table for pane workdirs. Remote URLs never leave
// the machine (02h "Repo identity"): only the normalized (host, path)
// does, and forge output is parsed for its fields, never logged whole.
package repos
