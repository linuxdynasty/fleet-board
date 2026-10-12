package doctor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// ErrHubUntrusted reports that the hub failed the pinned-CA handshake: its
// chain does not hold the CA in identity.json, its name does not match, or
// it answered something that is not TLS. The handshake func in cmd returns
// it; the check turns it into a fail, while any other error is a warn.
var ErrHubUntrusted = errors.New("the hub did not present the pinned CA")

// hubHandshakeCheck dials the hub's addresses with a TLS handshake against
// the pinned CA only. It never sends a Connect call, which would displace a
// running agent's stream.
func hubHandshakeCheck(ctx context.Context, e *Env) Outcome {
	id, prob := e.loadIdentity()
	if prob != nil {
		return notEnrolled()
	}
	if e.opts.HubHandshake == nil {
		return unknown("no way to check the hub handshake is configured", "report this as a bug in fleet-agent doctor")
	}
	addrs := hubCandidates(e, id)
	if len(addrs) == 0 {
		return unknown("no hub address is known: no cached address and no configured addresses",
			"pass --hub host[:port] to fleet-agent enroll to give the hub's address")
	}
	untrusted := ""
	for _, a := range addrs {
		if err := e.opts.HubHandshake(ctx, a, id.CAPEM); err == nil {
			return ok("reached the hub at " + a + " (a TLS handshake against the pinned CA passed, without disturbing the running agent)")
		} else if errors.Is(err, ErrHubUntrusted) && untrusted == "" {
			untrusted = a
		}
	}
	if untrusted != "" {
		return fail("the hub at "+untrusted+" did not present the CA in identity.json",
			"check --hub and enroll with a token from that hub")
	}
	if len(addrs) == 1 {
		return warn("could not reach the hub at "+addrs[0],
			"check that the hub is up and the address is right; --hub on enroll sets the address, and a host firewall can hide a hub")
	}
	return warn(fmt.Sprintf("could not reach the hub at %d addresses, starting with %s", len(addrs), addrs[0]),
		"check that the hub is up and the address is right; --hub on enroll sets the address, and a host firewall can hide a hub")
}

// hubCandidates are the addresses the agent would try without discovery, in
// the order the loop tries them: the cached address, the live peer, then the
// hub's latest list or the enrolled one. Invalid entries are dropped.
func hubCandidates(e *Env, id identityFile) []string {
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		norm, err := conn.HubAddress(strings.ToLower(strings.TrimSpace(a)))
		if err != nil || norm == "" || seen[norm] {
			return
		}
		seen[norm] = true
		out = append(out, norm)
	}
	if data, prob := e.privateFile(filepath.Join(e.StateDir(), conn.HubAddressFile)); prob == nil {
		add(string(data))
	}
	if s, err := e.Status(); err == nil && s.Peer != "" {
		add(s.Peer)
	}
	if data, prob := e.privateFile(filepath.Join(e.StateDir(), conn.HubAddressesFile)); prob == nil {
		for line := range strings.Lines(string(data)) {
			if strings.TrimSpace(line) != "" {
				add(line)
			}
		}
	} else {
		for _, a := range id.HubAddresses {
			add(a)
		}
	}
	return out
}
