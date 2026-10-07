package buildinfo_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
)

func TestProgramRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no arguments prints name and version", args: nil, wantCode: 0, wantStdout: "fleetd v1.2.3\n"},
		{name: "version flag prints name and version", args: []string{"--version"}, wantCode: 0, wantStdout: "fleetd v1.2.3\n"},
		{name: "help prints usage", args: []string{"--help"}, wantCode: 0, wantStdout: "usage: fleetd [--version]\n"},
		{name: "short help prints usage", args: []string{"-h"}, wantCode: 0, wantStdout: "usage: fleetd [--version]\n"},
		{name: "unknown flag is refused", args: []string{"--bogus"}, wantCode: 2, wantStderr: "fleetd: unknown argument \"--bogus\"\nusage: fleetd [--version]\n"},
		{name: "extra argument after version is refused", args: []string{"--version", "x"}, wantCode: 2, wantStderr: "fleetd: unknown argument \"x\"\nusage: fleetd [--version]\n"},
		{name: "the first unknown argument is the one named", args: []string{"--bogus", "x"}, wantCode: 2, wantStderr: "fleetd: unknown argument \"--bogus\"\nusage: fleetd [--version]\n"},
		{name: "empty string argument is refused", args: []string{""}, wantCode: 2, wantStderr: "fleetd: unknown argument \"\"\nusage: fleetd [--version]\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			p := buildinfo.Program{Name: "fleetd", Version: "v1.2.3"}
			code := p.Run(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if got := stdout.String(); got != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tc.wantStdout)
			}
			if got := stderr.String(); got != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", got, tc.wantStderr)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

func TestRunReportsWriteFailure(t *testing.T) {
	t.Parallel()

	p := buildinfo.Program{Name: "fleetd", Version: "v1.2.3"}
	var stderr bytes.Buffer
	if code := p.Run([]string{"--version"}, failingWriter{}, &stderr); code != 1 {
		t.Errorf("exit code with a closed stdout = %d, want 1", code)
	}
	if got, want := stderr.String(), "fleetd: write: closed pipe\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if code := p.Run([]string{"--bogus"}, &bytes.Buffer{}, failingWriter{}); code != 2 {
		t.Errorf("exit code with a closed stderr and a bad argument = %d, want 2", code)
	}
}

func TestCurrentDefaultsToDev(t *testing.T) {
	t.Parallel()

	got := buildinfo.Current("fleet")
	if got.Name != "fleet" || got.Version != "dev" {
		t.Errorf("Current(%q) = %+v, want name fleet and version dev", "fleet", got)
	}
}

// TestLinkerSetsVersion builds each real binary with -ldflags -X, the way
// release builds will, and runs it.
func TestLinkerSetsVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three binaries")
	}
	t.Parallel()

	for _, name := range []string{"fleetd", "fleet-agent", "fleet"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bin := t.TempDir() + "/" + name
			build := exec.CommandContext(t.Context(), "go", "build", //nolint:gosec // G204: bin is this test's temp path and name is from the literal list above
				"-ldflags", "-X github.com/linuxdynasty/fleet-board/internal/buildinfo.Version=v9.9.9",
				"-o", bin, "github.com/linuxdynasty/fleet-board/cmd/"+name)
			build.Env = append(os.Environ(), "CGO_ENABLED=0")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("go build: %v\n%s", err, out)
			}
			out, err := exec.CommandContext(t.Context(), bin, "--version").Output() //nolint:gosec // G204: bin is the binary this test just built
			if err != nil {
				t.Fatalf("run %s --version: %v", name, err)
			}
			if got, want := strings.TrimSpace(string(out)), name+" v9.9.9"; got != want {
				t.Errorf("%s --version = %q, want %q", name, got, want)
			}
		})
	}
}
