# internal/hub/attach

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Tickets stay in memory only: a hub restart drops them, and every attach
  dies with the restart (HUB_CLOSED), so they never reach the store.
- Burns and closes are terminal and idempotent: a retried frame or a
  doubled close refuses instead of re-running.
- Output is never dropped and never guessed: past the high-water mark the
  bridge suspends the agent side; it never sheds or coalesces.
- A change here updates this README and `doc.go` in the same commit.
