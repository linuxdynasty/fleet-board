# internal/agent/logship

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Raw `pipe-pane` bytes stay in plain append files, never in a database
  (`fleet-sqlite-store`, 04 Q9). Frames, the spool, shipping, `logs.db`
  and the 0.15 sizing are row 25 and stay out of this package.
- One-shot tmux CLI calls go through `internal/proc` (caller `tmux`);
  never import `os/exec` here (the depguard rule refuses it).
- Every argument after the binary and `-S` is a separate element, never a
  shell string. The pipe command is one argv element: the path inside is
  tmux-escaped (`%` to `%%`, `#` to `##`) and then single-quoted, because
  tmux expands status-left formats in it. Probe a new escape live on both
  tmux releases before trusting it.
- Pane ids are `%N` only; anything else is refused before any tmux call
  or file touch. No hub text reaches the filesystem: every path element
  is agent-built from charset-checked fields. Raw files are mode 0600:
  screens can hold secrets.
- Behavior lands with a unit test against the `Runner` seam plus a live
  test against a real `tmuxtest` server proving bytes land in the file.
- A change here updates this file, `README.md` and `doc.go` together, and
  touches `docs/specs/01f-agent-control-goals-and-policy.md` (the doc
  map points this folder at it).
