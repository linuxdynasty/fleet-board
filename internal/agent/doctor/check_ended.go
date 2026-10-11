package doctor

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// The connection check says "the agent will not reconnect" only from what
// `fleet-agent run` itself reads before it dials: ended.json for revoked,
// unknown machine and another fleet, and the certificate for an expired one.
// status.json also keeps the end the loop stopped on, but it is only a note:
// a re-enroll or the owner can clear the end later and leave the note behind.

// endedFileOutcome judges ended.json. done is false when there is no file.
func endedFileOutcome(e *Env) (out Outcome, done bool) {
	rec, err := e.Ended()
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return Outcome{}, false
	default:
		return fail("ended.json is damaged, from another version or open to other users: run exits 1 before any network call",
			"remove it to try again (ended.json in the state folder); fleet-agent enroll removes it too"), true
	}
	// With no readable identity the machine id cannot be compared; the
	// identity check reports its own failure and this end still stands.
	if id, prob := e.loadIdentity(); prob == nil && rec.MachineID != id.MachineID {
		return unknown("ended.json is a leftover from an earlier enrollment (it names another machine id); the next run removes it",
			"start the agent with fleet-agent run"), true
	}
	where := "ended.json says so"
	if rec.Peer != "" {
		where += ", from the hub at " + rec.Peer
	}
	where += ", " + ago(e.Now(), rec.Time)
	return endedOutcome(string(rec.End), where), true
}

// endedOutcome is a connection loop that stopped on a state only the owner
// can fix (03 W1); where says how doctor knows.
func endedOutcome(end, where string) Outcome {
	switch end {
	case "revoked", "unknown_machine":
		what := "the hub revoked this machine"
		if end == "unknown_machine" {
			what = "the hub does not know this machine"
		}
		return fail(fmt.Sprintf("%s (%s; %s): the agent stopped and will not reconnect", what, end, where),
			"get a new token from the dashboard, remove only identity.json (keep agent.db), then fleet-agent enroll --token <new token>")
	case "fleet_mismatch":
		return fail(fmt.Sprintf("the hub at the address in use belongs to another fleet (fleet_mismatch; %s): the agent stopped and will not reconnect", where),
			"check the hub address; if it is right, re-enroll with a token from that hub, or remove ended.json to try again")
	}
	return fail("the agent stopped in an end this doctor does not know (unknown)", "use the same version of fleet-agent and fleet-agent doctor")
}

// statusEndOutcome is the end status.json names when no ended.json does.
// The three ends ended.json keeps are gone, so the next run will try again.
// An expired certificate is judged from the certificate itself, as run does.
func statusEndOutcome(e *Env, s statusfile.Status) Outcome {
	stopped := fmt.Sprintf("the agent stopped on %s %s", s.Ended, ago(e.Now(), s.WrittenTime))
	switch s.Ended {
	case "revoked", "unknown_machine", "fleet_mismatch":
		return unknown(stopped+"; that end has since been cleared (a re-enroll or by hand), so the next run will try again",
			"start the agent with fleet-agent run")
	case "cert_expired":
		life, prob := lastEndingCert(e)
		switch {
		case prob != nil || life == nil:
			return unknown(stopped+", and identity.json holds no certificate doctor can read, so it cannot tell whether that still holds",
				"see the identity and cert_expiry checks")
		case !life.notAfter.After(e.Now()):
			return fail("the certificate that ends last ran out (cert_expired): the agent stopped, and run will not connect with it",
				"re-enroll: remove only identity.json (keep agent.db), then fleet-agent enroll --token <new token>")
		}
		return unknown(stopped+"; the certificate that ends last is valid now (re-enrolled since), so the next run will start",
			"start the agent with fleet-agent run")
	}
	return unknown("status.json names an end this doctor does not know (unknown), and no ended.json says the agent will not reconnect",
		"use the same version of fleet-agent and fleet-agent doctor")
}
