# internal/hostname

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`. Rules for
this package:

- One pure function; standard library only, no cgo.
- Hub and agent both import this package; never copy the rule elsewhere.
- Changing the rule changes which machines the hub flags: update 03 W1 and
  this README and `doc.go` in the same commit, with a case in the table test.
