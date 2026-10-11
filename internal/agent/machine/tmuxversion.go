package machine

import (
	"context"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// TmuxVersion runs `tmux -V` the way the machine facts do (the proc caller
// "tmux", PATH first, then dirs; nil dirs is [DefaultTmuxDirs]) and returns
// the first line, for example "tmux 3.4". tmux not installed is an answer,
// not an error: the version is empty. Doctor uses it so that it and the facts
// can never name different programs.
func TmuxVersion(ctx context.Context, r *proc.Runner, dirs []string) (string, error) {
	if dirs == nil {
		dirs = DefaultTmuxDirs
	}
	return tmuxVersion(ctx, r, dirs)
}
