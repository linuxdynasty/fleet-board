# internal/agent/tmux/inventory

Read the root `AGENTS.md` and `internal/agent/tmux/AGENTS.md` first; they
apply here. Load `fleet-go`, `fleet-tmux`, `fleet-security` and
`fleet-telemetry-coverage`. Rules for this package:

- The tree build and the diff are pure functions: no tmux, no clock, no
  goroutine, no lock.
- No timer reads tmux or the socket folder (row-7 ruling fork 4): a read
  follows a control-mode notice, a client state change, a Snapshot or a
  folder event. The two timers, the wake debounce (`wakeGap`) and the read
  pacing (`readGap`), only delay what an event already asked for. One
  bounded exception: a pane read dead with no exit status is owed
  `deadStatusPollMax` follow-up reads one second apart, until its status
  lands or the cap runs out — the death's own notice asked for the status
  (Linux sends no notice when only the status lands), so the timer still
  only delays what that notice asked for, never polls for anything new.
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
