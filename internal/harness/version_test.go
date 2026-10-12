package harness_test

import (
	"context"
	"os/exec"
	"regexp"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/harness"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// The expected versions below are the real outputs measured on macOS arm64,
// 2026-10-12: `claude --version` prints "2.1.296 (Claude Code)",
// `codex --version` prints "codex-cli 0.162.1", `cursor-agent --version`
// prints "2026.10.01-e373342", `opencode --version` prints "1.19.0".
func TestParseVersionOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		output     string
		wantVer    string
		wantReason harness.Reason
	}{
		{"claude", "2.1.296 (Claude Code)\n", "2.1.296", ""},
		{"codex", "codex-cli 0.162.1\n", "0.162.1", ""},
		{"cursor", "2026.10.01-e373342\n", "2026.10.01", ""},
		{"opencode", "1.19.0\n", "1.19.0", ""},
		{"no trailing newline", "1.19.0", "1.19.0", ""},
		{"extra lines after", "1.19.0\nsecond line 2.0.0\n", "1.19.0", ""},
		{"empty", "", "", harness.ReasonEmptyOutput},
		{"blank lines only", "  \n \n", "", harness.ReasonEmptyOutput},
		{
			"install prompt that exits 0 is not a version",
			"Welcome to Claude Code.\nRun the installer to continue.\n",
			"",
			harness.ReasonInvalidOutput,
		},
		{
			"version past the first line does not count",
			"Starting up, please wait.\n1.19.0\n",
			"",
			harness.ReasonInvalidOutput,
		},
		{"words and no number", "no version here\n", "", harness.ReasonInvalidOutput},
		{"single number is not dotted", "build 42\n", "", harness.ReasonInvalidOutput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := harness.ParseVersionOutput(tc.output)
			if got.Version != tc.wantVer || got.Reason != tc.wantReason {
				t.Errorf("ParseVersionOutput(%q) = %+v, want version %q reason %q",
					tc.output, got, tc.wantVer, tc.wantReason)
			}
			if (got.Reason == "") != (tc.wantReason == "") {
				t.Errorf("ParseVersionOutput(%q).OK() = %v, want %v",
					tc.output, got.OK(), tc.wantReason == "")
			}
		})
	}
}

// fakeRun is the seam between Detect and the child process: it records the
// spec Detect built and returns scripted output.
type fakeRun struct {
	spec   proc.Spec
	result proc.Result
	err    error
}

func (f *fakeRun) run(_ context.Context, spec proc.Spec) (proc.Result, error) {
	f.spec = spec
	return f.result, f.err
}

func testDetector(f *fakeRun) harness.Detector {
	caller, err := proc.For("version")
	if err != nil {
		panic(err)
	}
	return harness.Detector{Run: f.run, Caller: caller}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		result     proc.Result
		err        error
		wantVer    string
		wantReason harness.Reason
	}{
		{
			"version on stdout",
			proc.Result{Stdout: []byte("2.1.296 (Claude Code)\n")},
			nil,
			"2.1.296",
			"",
		},
		{
			"binary not on the path",
			proc.Result{},
			proc.ErrNotFound,
			"",
			harness.ReasonNotOnPath,
		},
		{
			"nothing printed",
			proc.Result{},
			nil,
			"",
			harness.ReasonEmptyOutput,
		},
		{
			"install prompt that exits 0",
			proc.Result{Stdout: []byte("Welcome. Run the installer to continue.\n")},
			nil,
			"",
			harness.ReasonInvalidOutput,
		},
		{
			"non-zero exit with a version still counts",
			proc.Result{Stdout: []byte("1.19.0\n"), ExitCode: 1},
			&proc.ExitError{Code: 1},
			"1.19.0",
			"",
		},
		{
			"time cap with no output yet",
			proc.Result{},
			proc.ErrTimeCap,
			"",
			harness.ReasonEmptyOutput,
		},
		{
			"version on stderr when stdout is blank",
			proc.Result{Stderr: []byte("0.162.1\n")},
			nil,
			"0.162.1",
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeRun{result: tc.result, err: tc.err}
			got := testDetector(f).Detect(context.Background(), "claude", "--version")
			if got.Version != tc.wantVer || got.Reason != tc.wantReason {
				t.Errorf("Detect() = %+v, want version %q reason %q", got, tc.wantVer, tc.wantReason)
			}
			if f.spec.Path != "claude" {
				t.Errorf("spec.Path = %q, want %q", f.spec.Path, "claude")
			}
			if len(f.spec.Args) != 1 || f.spec.Args[0] != "--version" {
				t.Errorf("spec.Args = %q, want [--version]", f.spec.Args)
			}
			if f.spec.Caller.Name() != "version" {
				t.Errorf("spec caller = %q, want %q", f.spec.Caller.Name(), "version")
			}
		})
	}
}

func TestNewDetectorResolvesTheVersionCaller(t *testing.T) {
	t.Parallel()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := harness.NewDetector(runner)
	if err != nil {
		t.Fatal(err)
	}
	if d.Caller.Name() != "version" {
		t.Errorf("caller = %q, want %q", d.Caller.Name(), "version")
	}
	if d.Run == nil {
		t.Error("Run is nil: NewDetector must wire the runner")
	}
}

// TestDetectThroughProc runs a real child through the real helper: `go
// version` prints a dotted toolchain version on its first line on every
// platform the tests run on, so it proves the run-to-parse path end to end.
func TestDetectThroughProc(t *testing.T) {
	t.Parallel()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := harness.NewDetector(runner)
	if err != nil {
		t.Fatal(err)
	}
	got := d.Detect(context.Background(), "go", "version")
	if !got.OK() {
		t.Fatalf("Detect(go version) = %+v, want a version", got)
	}
	raw, err := exec.CommandContext(context.Background(), "go", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := regexp.MustCompile(`\d+\.\d+(\.\d+)*`).FindString(string(raw))
	if got.Version != want {
		t.Errorf("Detect(go version) = %q, want %q", got.Version, want)
	}
}

func TestReasonStrings(t *testing.T) {
	t.Parallel()
	for reason, want := range map[harness.Reason]string{
		harness.ReasonNotOnPath:     "not_on_path",
		harness.ReasonEmptyOutput:   "empty_version_output",
		harness.ReasonInvalidOutput: "invalid_version_output",
	} {
		if string(reason) != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
	}
}
