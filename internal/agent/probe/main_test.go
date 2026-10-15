package probe

import (
	"os"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

func TestMain(m *testing.M) { os.Exit(tmuxtest.Main(m)) }
