package typing_test

import (
	"os"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// Every tmux package's tests run with TMUX unset and TMUX_TMPDIR an empty
// folder of their own, so no test can reach the developer's server.
func TestMain(m *testing.M) { os.Exit(tmuxtest.Main(m)) }
