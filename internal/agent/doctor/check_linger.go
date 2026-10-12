package doctor

import (
	"context"
	"runtime"
	"strings"
)

// lingerCheck reports whether the agent survives logout on Linux. The owner
// runs `sudo loginctl enable-linger <user>` once per worker (02j); doctor
// runs `loginctl show-user <user> --property=Linger` through Options.Linger
// and says how to fix it. On other systems there is nothing to check.
func lingerCheck(ctx context.Context, e *Env) Outcome {
	goos := e.opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return ok("linger is a Linux setting; this machine runs " + goos + ", nothing to do")
	}
	if e.opts.Linger == nil {
		return unknown("no way to check linger is configured", "report this as a bug in fleet-agent doctor")
	}
	out, err := e.opts.Linger(ctx)
	if err != nil {
		return unknown("could not read linger state", "run loginctl show-user <user> --property=Linger in a terminal to see why")
	}
	for line := range strings.Lines(out) {
		v := strings.TrimSpace(line)
		if v == "Linger=yes" {
			return ok("linger is on: the agent keeps running after logout")
		}
		if v == "Linger=no" {
			user := e.opts.LingerUser
			if user == "" {
				user = "<user>"
			}
			return fail("linger is off, so the agent stops when the last session ends",
				"sudo loginctl enable-linger "+user)
		}
	}
	return unknown("could not read linger state", "run loginctl show-user <user> --property=Linger in a terminal to see why")
}
