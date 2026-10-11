package tmux_test

import (
	"os"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// sleeperEnv makes a copy of this test binary write the file the variable
// names, then only sleep: a test runs it in a pane under a file name tmux
// reads as pane_current_command (one with a newline). tmux's kill-server
// ends it.
const sleeperEnv = "FLEET_TMUX_TEST_SLEEPER"

// Every tmux package's tests run with TMUX unset and TMUX_TMPDIR an empty
// folder of their own, so no test can reach the developer's server.
func TestMain(m *testing.M) {
	if ready := os.Getenv(sleeperEnv); ready != "" {
		_ = os.WriteFile(ready, nil, 0o600) //nolint:gosec // G703: the path the test that started this copy chose, in its own folder
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(tmuxtest.Main(m))
}
