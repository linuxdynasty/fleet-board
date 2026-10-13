# repos

The agent's repository inventory (H1, H3, H4).

- `Discover` finds every git checkout under the allowed roots without
  descending into one it recorded.
- `Git.Inspect` reads one checkout: top, branch (empty when detached),
  HEAD, dirt (tracked and untracked alike), upstream ahead/behind, every
  remote normalized to `(host, path)`, and the worktree shape.
- `Forge` reads `gh auth status` (signed-in hosts only) and per-checkout
  `viewerPermission` (push access), re-asked at most every 10 minutes.
- `Refresher` rebuilds the table on every tick and fills the Snapshot's
  `repos` and `forge_cli`; `Workdir` maps a pane path to its checkout.
