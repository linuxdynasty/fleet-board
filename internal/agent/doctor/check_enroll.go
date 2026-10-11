package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

func isMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// enrollFixes is what to do for each closed code of a failed enroll
// (statusfile.Enroll). A code this doctor does not know gets the last line.
var enrollFixes = map[string]string{
	"refused":         "get a new token from the dashboard and run fleet-agent enroll --token <token>; the reason says what the hub did not accept",
	"hub_untrusted":   "the hub at that address does not hold the CA named in the token: check --hub and use a token from that hub",
	"bad_response":    "the hub's answer failed this agent's checks: run enroll again, and if it repeats check that the hub and the agent are the same version",
	"hub_failed":      "the hub had an error: run enroll again, and look at the hub's log if it repeats",
	"loose_mode":      "make the state folder private: chmod 700 on the folder and chmod 600 on the files in it (see the state_dir check)",
	"token_malformed": "copy the whole token, with no spaces or line breaks, into --token",
	"no_hub":          "give the hub with --hub host[:port]",
	"hub_address":     "--hub is not a valid host[:port]: fix it",
	"no_hub_found":    "no hub answered on the local network: pass --hub host[:port]; a host firewall (for example ufw with deny incoming) can hide the hub from mDNS",
	"timeout":         "the hub did not answer in time: check the network and the --hub address",
	"interrupted":     "enroll was stopped before it finished: run it again",
}

const enrollFixOther = "read what fleet-agent enroll printed when it failed, and run it again"

func enrollCheck(_ context.Context, e *Env) Outcome {
	rec, err := statusfile.ReadEnroll(e.StateDir())
	switch {
	case err == nil:
	case isMissing(err):
		return ok("no failed enroll recorded (the last enroll worked, or none ran)")
	case errors.Is(err, statusfile.ErrDamaged):
		return warn("enroll.json is damaged", "remove it; if this machine is not enrolled, run fleet-agent enroll again")
	case errors.Is(err, statusfile.ErrVersion):
		return unknown("enroll.json is from another version of fleet-agent", "run the same version of fleet-agent that wrote it")
	default:
		return unknown("enroll.json cannot be read", "check the permissions of the state folder")
	}
	fix, known := enrollFixes[rec.Code]
	if !known {
		fix = enrollFixOther
	}
	reason := fmt.Sprintf("the last enroll failed with %s, %s", rec.Code, ago(e.Now(), rec.WrittenTime))
	if rec.Reason != "" {
		reason += " (the hub's reason: " + rec.Reason + ")"
	}
	return fail(reason, fix)
}
