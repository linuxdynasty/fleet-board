package doctor

import (
	"context"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// pushSocketCheck reports the agent's own word about the network watcher's
// push socket (status.json: push_off, ADR 0029 decision 6). The agent says
// nothing when it is not running, so an old file is unknown.
func pushSocketCheck(_ context.Context, e *Env) Outcome {
	s, old, err := e.freshStatus()
	switch {
	case err != nil:
		return noStatus(err)
	case old:
		return unknown("status.json is old: the agent is not running, so its push socket is not known", "start the agent with fleet-agent run")
	case s.PushOff:
		return warn("the network watcher's push socket is off (push_off), so it looks for a network change by checking every 15 seconds instead of hearing it at once",
			"nothing is broken: a change on the network is noticed within 15 seconds. Restart the agent to try the push socket again; agent.log in the state folder says why it could not open")
	}
	return ok("the network watcher's push socket is on")
}

// mdnsBrowseCheck reports the last mDNS browse the agent made. The browse
// carries its own time, so like the clock it stays true when the file is old.
func mdnsBrowseCheck(_ context.Context, e *Env) Outcome {
	s, _, err := e.freshStatus()
	if err != nil {
		return noStatus(err)
	}
	b := s.Browse
	if b == nil {
		return unknown("no browse since the agent started: a cached or configured address was tried", "nothing to do; the agent browses only when those addresses fail")
	}
	when := ago(e.Now(), b.Time)
	counts := fmt.Sprintf("kept %d answers, dropped %d", b.Kept, b.Dropped)
	fix := "pass --hub <host[:port]> to fleet-agent enroll to give the hub's address; a firewall on this machine or the hub's machine can hide the hub from mDNS"
	switch b.Result {
	case statusfile.BrowseFound:
		return ok(fmt.Sprintf("the last mDNS browse found a hub %s (%s)", when, counts))
	case statusfile.BrowseNone:
		return warn(fmt.Sprintf("the last mDNS browse found no hub %s (%s)", when, counts), fix)
	case statusfile.BrowseNoMulticastInterface:
		return warn(fmt.Sprintf("the last mDNS browse %s had no network interface that can send multicast, so it asked nobody", when), fix)
	}
	return unknown("status.json holds a browse result this doctor does not know (unknown)", "use the same version of fleet-agent and fleet-agent doctor")
}
