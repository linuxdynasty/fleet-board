# internal/agent/goalregistry

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- The registry binds fleet-started goals to panes (02h "Goal registry").
  Liveness is the hub's call: Register replaces, the hub refuses against
  a live goal (row 22). Never guess a match: anything unmatched is silent.
- Hook payload fields are untrusted text: cap and clean at the boundary
  (`goalfile.Clean`), never log a session id raw, never report file text.
- The board bootstrap seam (`goalfile.Writer.EnsureBoard`) is lane 1's
  `internal/agent/worktree.EnsureBoardDir`; until the lanes merge every
  file step refuses (`ErrBoardUnwired`) instead of guessing.
- A change here updates this file, `README.md` and `doc.go` together.
