# internal/agent/tmux/inventory

Read the root `AGENTS.md` and `internal/agent/tmux/AGENTS.md` first; they
apply here. Load `fleet-go`, `fleet-tmux`, `fleet-security` and
`fleet-telemetry-coverage`. Rules for this package:

- The tree build and the diff are pure functions: no tmux, no clock, no
  goroutine, no lock. Row 7 PR 7d2 adds the observer that reads tmux and
  calls them, with its own rules here.
- Names, commands and tag values are the developer's text: bounded and
  control-stripped before they are reported (02e), never logged or used as
  a label. A tag is reported only with its id's shape, else dropped whole;
  it is never read from a process environment.
- Never type into a pane and never set a tmux option (principle 4): this
  package only reads.
- Tests start tmux only through `tmuxtest` (`Start`, `Sibling`), and the test
  binary's `TestMain` calls `tmuxtest.Main`. Never the developer's server.
- A change here updates this README, `doc.go`, 01 E1 to E3, the 02h
  inventory paragraphs and the 05b rows in the same commit.
