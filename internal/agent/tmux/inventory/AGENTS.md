# internal/agent/tmux/inventory

Read the root `AGENTS.md` and `internal/agent/tmux/AGENTS.md` first; they
apply here. Load `fleet-go`, `fleet-tmux`, `fleet-security` and
`fleet-telemetry-coverage`. Rules for this package:

- No timer reads tmux or the socket folder (row-7 ruling fork 4): a read
  follows a control-mode notice, a client state change, a Snapshot or a
  folder event. The one timer is the wake debounce (`wakeGap`), which only
  delays a wake an event already asked for.
- Only the sync goroutine of a server reads, diffs, stores and emits for it;
  `Notify` drops tokens and never calls the client. Nothing is held across a
  control-mode command or an `Emit`. A change to that updates the README
  diagram in the same commit.
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
