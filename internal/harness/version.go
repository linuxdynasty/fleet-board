package harness

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Reason says why a harness version command produced no version. It is one
// of the three names of 01 C1: a wrapper that exits 0 but prints an install
// prompt is not a version.
type Reason string

const (
	// ReasonNotOnPath is a version command whose program is not on the path.
	ReasonNotOnPath Reason = "not_on_path"
	// ReasonEmptyOutput is a version command that printed nothing.
	ReasonEmptyOutput Reason = "empty_version_output"
	// ReasonInvalidOutput is a version command whose first line holds no
	// dotted version number.
	ReasonInvalidOutput Reason = "invalid_version_output"
)

// Outcome is one version check: either a Version or the Reason there is
// none, never both. A missing version counts as a failed Detect (03e, W11):
// two in a row mark the harness MISSING.
type Outcome struct {
	Version string
	Reason  Reason
}

// OK is whether the check found a version.
func (o Outcome) OK() bool { return o.Reason == "" }

// versionNumber is a dotted version number such as 1.4.2 or 2026.10.01. A
// bare number with no dot is not a version.
var versionNumber = regexp.MustCompile(`\d+\.\d+(\.\d+)*`)

// ParseVersionOutput reads the first line of a version command's output. It
// returns the dotted version that line holds, or the reason there is none:
// empty output, or a first line with no dotted number (an install prompt that
// exits 0, a startup notice, anything but a version).
func ParseVersionOutput(output string) Outcome {
	first, _, _ := strings.Cut(output, "\n")
	if strings.TrimSpace(first) == "" {
		return Outcome{Reason: ReasonEmptyOutput}
	}
	if v := versionNumber.FindString(first); v != "" {
		return Outcome{Version: v}
	}
	return Outcome{Reason: ReasonInvalidOutput}
}

// Detector runs harness version commands through the one child-process
// helper (01 principle 8: empty stdin, a time cap and an output cap, a
// process group of its own). Run is the seam where a test observes the call
// without starting a child; NewDetector wires the real helper.
type Detector struct {
	Run    func(ctx context.Context, spec proc.Spec) (proc.Result, error)
	Caller proc.Caller
}

// NewDetector wires a Detector to runner under the "version" caller, whose
// caps the defaults row "One-shot child process" sets from measured runs of
// the real version commands.
func NewDetector(runner *proc.Runner) (Detector, error) {
	caller, err := proc.For("version")
	if err != nil {
		return Detector{}, fmt.Errorf("harness version caller: %w", err)
	}
	return Detector{Run: runner.Run, Caller: caller}, nil
}

// Detect runs binary with args (its version command) and reads the version
// from the first line of what it printed: stdout, then stderr when stdout is
// blank. A program that is not on the path yields not_on_path; any other
// failure yields whatever the captured output parses to, so a child that was
// killed after printing a version still reports it. The exit status is not
// consulted: a version printed on the way to a non-zero exit is still the
// harness's version.
func (d Detector) Detect(ctx context.Context, binary string, args ...string) Outcome {
	out, err := d.Run(ctx, proc.Spec{Caller: d.Caller, Path: binary, Args: args})
	if err != nil && errors.Is(err, proc.ErrNotFound) {
		return Outcome{Reason: ReasonNotOnPath}
	}
	stdout := string(out.Stdout)
	if strings.TrimSpace(stdout) == "" {
		stdout = string(out.Stderr)
	}
	return ParseVersionOutput(stdout)
}
