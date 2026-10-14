package doctor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// ago prints how long before now t was, whole seconds.
func ago(now, t time.Time) string { return max(now.Sub(t), 0).Round(time.Second).String() + " ago" }

func statusFileCheck(_ context.Context, e *Env) Outcome {
	s, err := e.Status()
	switch {
	case err == nil:
	case errors.Is(err, statusfile.ErrDamaged):
		return warn("status.json is damaged", "remove it; the running agent writes it again within a minute")
	case errors.Is(err, statusfile.ErrVersion):
		return unknown("status.json is from another version of fleet-agent", "run the same version of fleet-agent that writes it")
	case isMissing(err):
		return unknown("no status.json in "+e.StateDir()+": the agent has not run here, or never wrote it", "start the agent with fleet-agent run")
	default:
		return unknown("status.json cannot be read", "check the permissions of the state folder")
	}
	age := s.Age(e.Now())
	if age > StaleAfter {
		return warn("status.json was written "+ago(e.Now(), s.WrittenTime)+": the agent is not running (it writes at least once a minute) or cannot write",
			"start it with fleet-agent run (or its launchd or systemd unit); if it is running, agent.log in the state folder names the failed write")
	}
	return ok("status.json was written " + ago(e.Now(), s.WrittenTime))
}

// maxFailuresShown is how many per-address failures the connection reason lists.
const maxFailuresShown = 3

func connectionCheck(_ context.Context, e *Env) Outcome {
	if out, done := endedFileOutcome(e); done {
		return out
	}
	s, old, err := e.freshStatus()
	if err != nil {
		return noStatus(err)
	}
	if s.Ended != "" {
		return statusEndOutcome(e, s)
	}
	switch {
	case old:
		last := "no state"
		if s.State != "" {
			last = s.State
		}
		return unknown(fmt.Sprintf("status.json is %s old: the agent is not running; its last known state was %s", e.Now().Sub(s.WrittenTime).Round(time.Second), last), "start the agent with fleet-agent run")
	case s.State == "":
		return unknown("status.json does not say the connection state yet", "wait a few seconds and run doctor again")
	case s.State == statusfile.StateUnknown:
		return unknown("status.json holds a connection state this doctor does not know (unknown)", "use the same version of fleet-agent and fleet-agent doctor")
	case s.State == "connected":
		if s.Peer != "" {
			return ok("connected to " + s.Peer)
		}
		return ok("connected")
	}
	return warn(fmt.Sprintf("the agent is %s%s", strings.ReplaceAll(s.State, "_", " "), failureList(s)),
		"if it stays like this check that the hub is up and the address is right; --hub on enroll sets the address, and a host firewall can hide a hub from mDNS")
}

// failureList names the newest failures, one per hub address, with the
// closed step and kind words the agent wrote.
func failureList(s statusfile.Status) string {
	if len(s.Failures) == 0 {
		return ""
	}
	type row struct {
		addr string
		f    statusfile.Failure
	}
	rows := make([]row, 0, len(s.Failures))
	for a, f := range s.Failures {
		rows = append(rows, row{a, f})
	}
	newest := func(r row) time.Time {
		if r.f.Time != nil {
			return *r.f.Time
		}
		return time.Time{}
	}
	sort.Slice(rows, func(i, j int) bool {
		if ti, tj := newest(rows[i]), newest(rows[j]); !ti.Equal(tj) {
			return ti.After(tj)
		}
		return rows[i].addr < rows[j].addr
	})
	var parts []string
	for _, r := range rows[:min(len(rows), maxFailuresShown)] {
		parts = append(parts, fmt.Sprintf("%s %s/%s", r.addr, r.f.Step, r.f.ErrKind))
	}
	out := "; last failures: " + strings.Join(parts, "; ")
	if more := len(rows) - maxFailuresShown; more > 0 {
		out += fmt.Sprintf("; and %d more", more)
	}
	return out
}

// twoPaneFoldersCheck lists the checkout folders holding two harness
// panes (02h "Observed goals"): no goal is attributed there, so a goal
// started by hand in one of them never shows. One pane per checkout, or
// work in separate checkouts, fixes it.
func twoPaneFoldersCheck(_ context.Context, e *Env) Outcome {
	s, _, err := e.freshStatus()
	if err != nil {
		return noStatus(err)
	}
	if len(s.AmbiguousFolders) == 0 {
		return ok("no folder holds two harness panes")
	}
	return warn("no goal is observed in "+strings.Join(s.AmbiguousFolders, ", ")+
		": each holds two harness panes and the agent never guesses which one a goal file belongs to",
		"keep one harness pane per checkout, or work in separate checkouts")
}

func snapshotSizeCheck(_ context.Context, e *Env) Outcome {
	s, _, err := e.freshStatus()
	if err != nil {
		return noStatus(err)
	}
	if s.SnapshotOversizeTime == nil {
		return ok("no oversize snapshot recorded")
	}
	return warn(fmt.Sprintf("the agent's snapshot went over the 960 KiB cap %s and was not sent whole", ago(e.Now(), *s.SnapshotOversizeTime)),
		"the hub must be upgraded: it does not accept a snapshot in parts")
}

// Clock offsets: above warnSkew the hub warns about the machine, above
// failSkew it no longer trusts the machine's times (03 W1, 02i "Clocks").
const (
	warnSkew = 5 * time.Minute
	failSkew = 15 * time.Minute
)

func clockCheck(_ context.Context, e *Env) Outcome {
	s, _, err := e.freshStatus()
	if err != nil {
		return noStatus(err)
	}
	if s.Clock == nil {
		return unknown("the clock offset has not been measured yet (the agent has not completed a hello since it started)", "start the agent with fleet-agent run and connect it to the hub")
	}
	off := time.Duration(s.Clock.OffsetMS) * time.Millisecond
	text := fmt.Sprintf("hub time minus this machine's clock was %s, measured %s", off, ago(e.Now(), s.Clock.Time))
	fix := "turn on network time (Linux: timedatectl set-ntp true; macOS: System Settings, Date & Time, set automatically)"
	side := "this machine is behind"
	if off < 0 {
		side = "this machine is ahead"
	}
	switch abs := max(off, -off); {
	case abs > failSkew:
		return fail(text+" ("+side+"); above 15 minutes the hub no longer trusts this machine's times", fix)
	case abs > warnSkew:
		return warn(text+" ("+side+"); above 5 minutes the hub warns about the machine", fix)
	}
	return ok(text)
}
