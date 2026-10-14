package repos

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Host is one code-host CLI sign-in (H4): the tool, the host and the user.
// Only signed-in hosts are reported; anything else (no CLI, signed out,
// failure) is no entry, never an error, so the hub reads absence as
// unknown. Raw CLI output is parsed for these fields and never logged:
// token lines are dropped at the boundary.
type Host struct {
	Tool     string
	Host     string
	User     string
	SignedIn bool
}

// Access is the CLI's view of push access to one repo (H4).
type Access struct {
	Push       bool
	Permission string // as the provider names it (GitHub READ..ADMIN)
}

// Forge reports forge CLI state. Implementations run the real CLI (through
// internal/proc, caller "gh") or a stub for tests.
type Forge interface {
	Status(ctx context.Context) []Host
	// Access reads the push access of the checkout at top: gh learns the
	// repo from its remote, so GHE hosts need no extra flag. ok is false
	// when gh cannot answer.
	Access(ctx context.Context, top string) (Access, bool)
}

type realForge struct {
	runner *proc.Runner
	caller proc.Caller
}

// NewForge runs gh through the runner with the named caller ("gh").
func NewForge(runner *proc.Runner, caller proc.Caller) Forge {
	return realForge{runner: runner, caller: caller}
}

// Status lists the hosts gh is signed into. Any failure (no gh, signed
// out, network) is no entry: the hub cannot tell those apart and must
// not read absence as signed out.
func (f realForge) Status(ctx context.Context) []Host {
	res, err := f.runner.Run(ctx, proc.Spec{Caller: f.caller, Path: "gh", Args: []string{"auth", "status"}})
	if err != nil {
		return nil
	}
	return parseAuthStatus(string(res.Stdout))
}

// Access reads viewerPermission for the checkout at top (gh reads its
// remote, so the host needs no flag). ok is false when gh cannot answer
// (rate limit, private repo, offline): the caller keeps its last reading.
func (f realForge) Access(ctx context.Context, top string) (Access, bool) {
	res, err := f.runner.Run(ctx, proc.Spec{
		Caller: f.caller, Path: "gh", Dir: top,
		Args: []string{"repo", "view", "--json", "viewerPermission", "-q", ".viewerPermission"},
	})
	if err != nil {
		return Access{}, false
	}
	perm, push, ok := parseViewerPermission(string(res.Stdout))
	if !ok {
		return Access{}, false
	}
	return Access{Push: push, Permission: perm}, true
}

// parseAuthStatus reads `gh auth status`: a host line (no indent) followed
// by an indented "✓ Logged in to <host> account <user>" line. Lines under
// a host that do not log in are ignored, and so are the "- Token" lines:
// only the host and the user cross the boundary.
func parseAuthStatus(out string) []Host {
	var hosts []Host
	var host string
	for line := range strings.Lines(out) {
		trimmed := strings.TrimRight(line, "\n")
		if trimmed == "" || strings.HasPrefix(trimmed, " ") || strings.HasPrefix(trimmed, "\t") {
			const mark = "Logged in to "
			if i := strings.Index(trimmed, mark); i >= 0 && host != "" {
				rest := strings.TrimSpace(trimmed[i+len(mark):])
				parts := strings.Fields(rest)
				if len(parts) >= 3 && parts[1] == "account" {
					hosts = append(hosts, Host{Tool: "gh", Host: host, User: parts[2], SignedIn: true})
					host = ""
				}
			}
			continue
		}
		host = strings.TrimSpace(trimmed)
	}
	return hosts
}

// parseViewerPermission reads the permission Word gh printed (with -q the
// output is the bare word). WRITE and above push.
func parseViewerPermission(out string) (perm string, push bool, ok bool) {
	word := strings.TrimSpace(out)
	if strings.HasPrefix(word, "{") {
		var v struct {
			ViewerPermission string `json:"viewerPermission"`
		}
		if err := json.Unmarshal([]byte(word), &v); err != nil || v.ViewerPermission == "" {
			return "", false, false
		}
		word = v.ViewerPermission
	}
	switch word {
	case "ADMIN", "MAINTAIN", "WRITE":
		return word, true, true
	case "READ", "TRIAGE":
		return word, false, true
	default:
		return "", false, false
	}
}
