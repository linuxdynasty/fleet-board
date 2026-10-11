# internal/agent/tmux/tmuxtest

Read the root `AGENTS.md` and `internal/agent/tmux/AGENTS.md` first; they
apply here. Load `fleet-go` and `fleet-tmux`. Rules for this package:

- Test support only: only `_test.go` files import it (the `tmuxtest`
  depguard rule in `.golangci.yml`, seeded in
  `scripts/test-lint-typing-rules.sh`). It never runs tmux
  without `-S` in a folder it made and `-f /dev/null`, and every server it
  starts is killed and checked gone (`has-session` fails) when its test ends.
- No `os/exec` here (depguard): one-shot tmux calls go through
  `internal/proc`; a long-lived client is started by the test file.
- A missing tmux skips with a message, or fails under `FLEET_REQUIRE_TMUX=1`;
  never a silent pass.
- A change here updates this README and `doc.go` in the same commit.
