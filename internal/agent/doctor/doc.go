// Package doctor is the engine of `fleet-agent doctor`: a list of named checks,
// each returning one of five statuses (ok, warn, fail, unknown, not_checked)
// with a reason and, unless it is ok, a fix (plan row 6, M3).
//
// The local checks need no network and no running agent. They read the state
// folder's files (status.json and enroll.json through internal/agent/statusfile,
// identity.json, cert.pem and renew.key for their dates and modes), the config
// folder's policy.yaml (policy.Inspect, with no side effect), and run `tmux -V`
// through the agent's measured proc caller. They never open agent.log or
// agent.db and never read a private key. The probe-trust check is the one
// exception to read-only: it starts each installed harness once in the probe
// folder on a throwaway tmux server, reads the first screen, sends no key
// and tears the session down (01 C3). Checks that need a later row are
// added by that row; until then they are listed as not_checked and name
// their row.
//
// [Run] cleans and bounds every string of the report before it is printed:
// policy errors, hub addresses and file names come from files an attacker might
// have written. [WriteText] and [WriteJSON] print it.
package doctor
