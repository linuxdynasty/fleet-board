package preflight

import (
	"context"
	"fmt"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// DiskLowBytes is the agent low-disk point: DISK_LOW fires when free bytes
// under a repo root fall below 5 GiB (defaults.md, ADR 0024).
const DiskLowBytes = 5 * 1024 * 1024 * 1024

// detailCap bounds one failure detail line (fleet-security: untrusted
// text is data, capped and stripped at the boundary).
const detailCap = 200

// Facts is one read-only snapshot of everything I1 checks. A Reader
// supplies it without acting: it reads the harness identity, the checkout
// table, the forge status, the harness screen trust bit and the disk
// sample, and never types a key.
type Facts struct {
	// SignedInKnown says a sign-in reading exists for the goal's
	// harness; HarnessSignedIn is its verdict. Unknown is skipped,
	// never failed: the agent has no sign-in source before a harness
	// screen is read, and the hub gates dispatch on its own account
	// knowledge meanwhile.
	SignedInKnown   bool
	HarnessSignedIn bool
	// CheckoutFound names a checkout of the goal's repo under an allowed root.
	CheckoutFound bool
	// PushKnown says the forge status for the repo is fresh enough to
	// judge; PushAccess is its verdict. Unknown is skipped, never
	// failed (the never-guess rule).
	PushKnown  bool
	PushAccess bool
	// FolderUntrusted is the harness's own screen showing a folder-trust
	// prompt, learned from that screen only, never from a trust file
	// (01 C3, ADR 0024).
	FolderUntrusted bool
	// DiskKnown says a disk sample under the repo root exists;
	// DiskFreeBytes is its free count. Unknown is skipped, never failed.
	DiskKnown     bool
	DiskFreeBytes uint64
}

// Reader supplies one Facts snapshot for a goal spec. It reads only.
type Reader interface {
	Facts(ctx context.Context, spec *agentv1.GoalSpec) (Facts, error)
}

// Typer is the only key-typing path the checker can see. Check never
// calls it; the tests hold a counting fake and prove the count stays
// zero on every path, refusal or clear.
type Typer interface {
	TypeKeys(ctx context.Context, text string) error
}

// Checker runs the five I1 preflight checks.
type Checker struct {
	Reader Reader
	Typer  Typer
}

// Check runs every check and reports each failure as its named reason in
// fixed order: sign-in, checkout, push access, folder trust, disk. A
// passing run reports ok with no rows. Unknown push-access or disk
// readings are skipped, never failed. It types no key on any path.
func (c *Checker) Check(ctx context.Context, spec *agentv1.GoalSpec) (*agentv1.PreflightResult, error) {
	if spec == nil {
		return nil, fmt.Errorf("preflight: nil goal spec")
	}
	if c == nil || c.Reader == nil {
		return nil, fmt.Errorf("preflight: no fact reader")
	}
	facts, err := c.Reader.Facts(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("preflight: read facts: %w", err)
	}
	harness := clean(spec.GetHarness().GetName())
	repo := clean(repoName(spec))
	res := &agentv1.PreflightResult{Ok: true}
	fail := func(why typev1.ReasonCode, retryable bool, detail string) {
		res.Ok = false
		res.FailedChecks = append(res.FailedChecks, &agentv1.PreflightFailure{
			Reason:    why,
			Detail:    detail,
			Retryable: retryable,
		})
	}
	if facts.SignedInKnown && !facts.HarnessSignedIn {
		fail(typev1.ReasonCode_REASON_CODE_HARNESS_NOT_SIGNED_IN, false,
			fmt.Sprintf("harness %q shows no signed-in account; sign in on the machine", harness))
	}
	if !facts.CheckoutFound {
		fail(typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING, false,
			fmt.Sprintf("no checkout of %s under the allowed roots", repo))
	}
	if facts.PushKnown && !facts.PushAccess {
		fail(typev1.ReasonCode_REASON_CODE_PUSH_ACCESS_MISSING, false,
			fmt.Sprintf("the signed-in forge user cannot push to %s", repo))
	}
	if facts.FolderUntrusted {
		fail(typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED, false,
			fmt.Sprintf("a folder-trust screen shows for harness %q; the owner answers it by hand", harness))
	}
	if facts.DiskKnown && facts.DiskFreeBytes < DiskLowBytes {
		fail(typev1.ReasonCode_REASON_CODE_DISK_LOW, true,
			fmt.Sprintf("only %d bytes free under the repo root, below 5 GiB", facts.DiskFreeBytes))
	}
	return res, nil
}

// repoName renders host/path for a detail line; the Ref carries no URL,
// so no credential can leak through it.
func repoName(spec *agentv1.GoalSpec) string {
	host, path := spec.GetRepo().GetHost(), spec.GetRepo().GetPath()
	switch {
	case host != "" && path != "":
		return host + "/" + path
	case path != "":
		return path
	default:
		return "the goal repo"
	}
}

// clean strips control characters and caps one detail line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > detailCap {
		s = s[:detailCap]
	}
	return s
}
