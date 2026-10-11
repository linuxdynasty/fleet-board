package doctor

import (
	"context"
	"regexp"
	"strconv"
)

// tmuxVersionRE reads the numbers out of `tmux -V`: "tmux 3.4", "tmux 3.2a",
// "tmux next-3.7", "tmux openbsd-7.5".
var tmuxVersionRE = regexp.MustCompile(`^tmux (?:[a-z]+-)?(\d+)\.(\d+)`)

// Minimum tmux (ADR 0020, runtime prerequisites).
const (
	minTmuxMajor = 3
	minTmuxMinor = 2
)

const tmuxInstall = "install tmux 3.2 or newer (macOS: brew install tmux; Debian or Ubuntu: apt install tmux)"

func tmuxCheck(ctx context.Context, e *Env) Outcome {
	if e.opts.TmuxVersion == nil {
		return unknown("no way to run tmux -V is configured", "report this as a bug in fleet-agent doctor")
	}
	line, err := e.opts.TmuxVersion(ctx)
	if err != nil {
		return unknown("could not run tmux -V", "run tmux -V in a terminal to see why")
	}
	if line == "" {
		return fail("tmux was not found on PATH or in /usr/local/bin, /opt/homebrew/bin or /usr/bin", tmuxInstall)
	}
	m := tmuxVersionRE.FindStringSubmatch(line)
	if m == nil {
		return unknown("tmux is installed but its version text is not recognised", "run tmux -V and check that it is 3.2 or newer")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minTmuxMajor || (major == minTmuxMajor && minor < minTmuxMinor) {
		return fail(line+" is older than the 3.2 the agent needs", tmuxInstall)
	}
	return ok(line + " is installed (3.2 or newer is needed)")
}
