# internal/agent/hooks

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- G1 only: user-level hook files and the scripts under the agent data
  dir. The goal registry, the goal file and hub RPCs belong to other
  lanes; this package defines the registry-file and spool shapes they
  share, and nothing more.
- The agent writes only its own named entries (commands pointing into
  the fleet hooks directory), backs the user file up first, and never
  approves hooks. Untrusted text (payloads, goal files, configs) is
  capped and stripped at the boundary; paths from payloads are never
  trusted further than a registry match.
- Never read a credential or auth file; the hook trust state comes from
  the harness rules in 01d, never from a trust store.
- One shared definition: a new harness event is a row in `Events` plus
  fixtures and tests, never a second script. `hook.py` is the one
  script; it stays macOS system-python3 compatible, exits 0 always, and
  prints only a block answer, never anything else.
- A change here updates this README and `doc.go` in the same commit.
