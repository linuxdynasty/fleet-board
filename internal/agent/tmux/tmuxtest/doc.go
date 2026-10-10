// Package tmuxtest is the only way a test starts tmux (plan row 7). It is
// imported by test files only.
//
// [Start] makes a throwaway server: a socket in a new short temp folder
// (t.TempDir is too long for a macOS socket path), -S, -f /dev/null, an
// environment without TMUX, and a cleanup that runs kill-server and fails the
// test if has-session still finds it. [Binaries] names the two releases a
// test runs against, from FLEET_TMUX_32 and FLEET_TMUX_LATEST (the tmux on
// PATH stands in for the latest); a missing one skips, or fails under
// FLEET_REQUIRE_TMUX=1. [Main] is the TestMain body: it runs [Sweep] (kill
// the servers a dead test process left, found by the [OwnerFile] in their
// folder) and points TMUX_TMPDIR at an empty folder for the whole test
// binary. One-shot tmux commands go
// through internal/proc; a test file that needs a long-lived client (a person
// at the keyboard on a PTY) starts it itself with [Server.Argv] and
// [Server.Env].
package tmuxtest
