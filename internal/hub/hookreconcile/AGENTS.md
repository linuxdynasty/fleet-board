# internal/hub/hookreconcile

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Pure decision plus a snapshot applier. `Decide` never touches the
  store or the network; the applier runs inside the snapshot's writer
  job (one writer per database) and enqueues inside that transaction.
- Never guess: unknown repos, disabled repos, unlaunchable harnesses,
  live goals and ESHU checkouts want nothing, even forced.
- No machine paths cross from hub to agent: wants address the repo
  short name; the agent joins it to its allowed roots.
- A change here updates this file, `README.md` and `doc.go` together.
