package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// maxListed is how many roots or harness names a reason lists.
const maxListed = 5

func listed(names []string) string {
	shown := names[:min(len(names), maxListed)]
	s := strings.Join(shown, ", ")
	if more := len(names) - len(shown); more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// policyFileCheck reads policy.yaml fresh, with no side effect: the file
// the agent would load at its next start, not necessarily the one it runs on.
func policyFileCheck(_ context.Context, e *Env) Outcome {
	in := e.Policy()
	if in.Present {
		if p, err := stat(in.Path); err == nil && p.Regular && p.perm()&0o022 != 0 {
			return fail(fmt.Sprintf("policy.yaml is mode %s: other users could change it and loosen this machine's ceiling", modeText(p)),
				"chmod go-w "+shellQuote(in.Path))
		}
	}
	switch {
	case !in.Present:
		return ok("no policy.yaml in " + e.ConfigDir() + ": a normal first run; the built-in defaults are in force (allowed_repo_roots empty, every harness allowed)")
	case in.Problem != nil:
		return fail(fmt.Sprintf("policy.yaml puts the agent in safe mode (%s): %s. In safe mode it refuses every control command and every hub setting", in.Problem.Code, in.Problem.Error()),
			"fix the line named above in "+shellQuote(in.Path)+"; the agent reads the file again at its next start")
	}
	return ok(fmt.Sprintf("policy.yaml is valid (schema_version %s, hash %s)", in.SchemaVersion, short(in.Hash)))
}

func short(hash string) string { return hash[:min(len(hash), 12)] }

func safeMode(e *Env) bool { return e.Policy().Problem != nil }

func policyRootsCheck(_ context.Context, e *Env) Outcome {
	if safeMode(e) {
		return unknown("policy.yaml is in safe mode, so no allowed_repo_roots are in force", "see the policy_file check")
	}
	roots := e.Policy().Policy.AllowedRepoRoots
	if len(roots) == 0 {
		return warn("allowed_repo_roots is empty: the agent inventories no repo, reports no working folder and reads no goal file (it never guesses a folder)",
			"add allowed_repo_roots to "+shellQuote(e.Policy().Path)+" (fleet-agent enroll --repos writes it into a new file)")
	}
	return ok(fmt.Sprintf("%d repo folder(s) allowed: %s", len(roots), listed(roots)))
}

func policyHarnessesCheck(_ context.Context, e *Env) Outcome {
	if safeMode(e) {
		return unknown("policy.yaml is in safe mode, so no allowed_harnesses are in force", "see the policy_file check")
	}
	h := e.Policy().Policy.AllowedHarnesses
	switch {
	case h == nil:
		return ok("no local opinion: every harness the contract knows is allowed until the adapters report what is installed (plan row 8)")
	case len(h) == 0:
		return warn("allowed_harnesses is an empty list: no harness may be started on this machine", "remove the key to allow every harness, or list the harness names")
	}
	return ok("allowed: " + listed(h))
}

// policyWatchCheck reports the agent's own word about its watch on the
// config folder (status.json: policy_watch). A stopped agent says nothing.
func policyWatchCheck(_ context.Context, e *Env) Outcome {
	s, old, err := e.freshStatus()
	switch {
	case err != nil:
		return noStatus(err)
	case s.PolicyWatch == statusfile.PolicyWatchOff:
		return warn("the agent could not set its watch on the config folder (policy_watch: off), so a change to policy.yaml is noticed only when a snapshot is built or a settings message arrives",
			"raise the limit (on Linux fs.inotify.max_user_instances or fs.inotify.max_user_watches), then restart the agent")
	case old:
		return unknown("status.json is old: the agent is not running, so its watch is not known", "start the agent with fleet-agent run")
	}
	return ok("the agent reported no problem with its policy watch")
}
