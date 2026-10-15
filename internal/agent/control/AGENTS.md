# internal/agent/control

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- One-shot tmux CLI calls go through `internal/proc` (caller `tmux`); never
  import `os/exec` here (the depguard rule refuses it).
- Every argument after the binary and `-S` is a separate element, never a
  shell string; names from the hub are re-validated before use.
- Nothing here types into a pane. Keystrokes stay in the `SendKeys` slice,
  built by `internal/agent/tmux/typing` only. The one exception is
  `Interrupt`'s fixed C-c (F4): a constant control key, not operator text,
  sent as one-shot CLI argv with a re-validated pane id (01f F4).
- Behavior lands with a test against a real `tmuxtest` server; a refusal
  names its status and reason in the test.
- A change here updates this README and `doc.go` in the same commit, and
  touches `docs/specs/01f-agent-control-goals-and-policy.md` (the doc map
  points this folder at it).
