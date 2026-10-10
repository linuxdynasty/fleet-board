//go:build unix

package machine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// pathWith puts a fake `tmux` script first (and only) on PATH. These tests
// change the process environment, so none of them is parallel.
func pathWith(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { //nolint:gosec // G306: a test script must be executable.
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	if script == "" {
		t.Setenv("PATH", dir) // nothing named tmux anywhere
	}
}

// noDirs turns off the fallback directories, so a tmux installed on the
// machine running the test cannot be found.
func noDirs() machine.CollectorOptions { return machine.CollectorOptions{TmuxDirs: []string{}} }

func collect(t *testing.T) (*agentv1.MachineFacts, *lockedBuffer, string) {
	t.Helper()
	return collectWith(t, noDirs())
}

func collectWith(t *testing.T, o machine.CollectorOptions) (*agentv1.MachineFacts, *lockedBuffer, string) {
	t.Helper()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	tel, m, logs := newTel(t)
	o.Runner, o.Metrics = runner, m
	c, err := machine.NewCollector(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	f := &agentv1.MachineFacts{}
	c.Fill(t.Context(), f)
	return f, logs, scrape(t, tel)
}

func TestTmuxVersionIsWhatTmuxPrintsThroughTheProcHelper(t *testing.T) {
	pathWith(t, `echo "tmux 9.9z"`)
	f, logs, _ := collect(t)
	if f.GetTmuxVersion() != "tmux 9.9z" {
		t.Fatalf("tmux_version = %q", f.GetTmuxVersion())
	}
	if ev := logs.events("sample.failed"); len(ev) != 0 {
		t.Fatalf("a working tmux logged a failure: %v", ev)
	}
}

// No tmux on PATH is an answer: the version is empty and nothing failed.
func TestMissingTmuxLeavesTheVersionEmptyWithoutAFailure(t *testing.T) {
	pathWith(t, "")
	f, logs, body := collect(t)
	if f.GetTmuxVersion() != "" {
		t.Fatalf("tmux_version = %q, want empty", f.GetTmuxVersion())
	}
	if ev := logs.events("sample.failed"); len(ev) != 0 {
		t.Fatalf("a missing tmux is not a failed reading: %v", ev)
	}
	if v := seriesValue(body, "fleet_agent_sample_failures_total{", `source="tmux"`); v != "" {
		t.Fatalf("a missing tmux was counted as a failure: %q", v)
	}
}

func TestTmuxThatFailsOrPrintsGarbageIsAFailedReading(t *testing.T) {
	for name, script := range map[string]string{
		"garbage":  `echo "screen 4.9"`,
		"exit 1":   `echo "tmux 3.4"; exit 1`,
		"escape":   `printf 'tmux 3.\033[2J'`,
		"no print": `exit 0`,
	} {
		t.Run(name, func(t *testing.T) {
			pathWith(t, script)
			f, logs, body := collect(t)
			if f.GetTmuxVersion() != "" {
				t.Fatalf("tmux_version = %q, want empty", f.GetTmuxVersion())
			}
			if ev := logs.events("sample.failed"); len(ev) != 1 || ev[0]["source"] != "tmux" {
				t.Fatalf("want one sample.failed for tmux, got %v", ev)
			}
			if v := seriesValue(body, "fleet_agent_sample_failures_total{", `source="tmux"`); v != "1" {
				t.Fatalf("failure counter = %q, want 1", v)
			}
		})
	}
}

// The caller's context ends a tmux that hangs: the Snapshot hook does not
// wait on it.
func TestAHungTmuxEndsWithTheContext(t *testing.T) {
	pathWith(t, `sleep 30`)
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := machine.NewCollector(context.Background(), machine.CollectorOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	f := &agentv1.MachineFacts{}
	c.Fill(ctx, f)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("Fill took %v with a hung tmux", took)
	}
	if f.GetTmuxVersion() != "" {
		t.Fatalf("tmux_version = %q", f.GetTmuxVersion())
	}
}

// With no Runner (a build without the proc helper) the version is unknown.
func TestNoRunnerMeansNoTmuxVersion(t *testing.T) {
	c, err := machine.NewCollector(t.Context(), machine.CollectorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := &agentv1.MachineFacts{}
	c.Fill(t.Context(), f)
	if f.GetTmuxVersion() != "" {
		t.Fatalf("tmux_version = %q", f.GetTmuxVersion())
	}
}

// On a real machine the other facts are read too.
func TestTheRealMachineFacts(t *testing.T) {
	pathWith(t, `echo "tmux 3.4"`)
	f, _, _ := collect(t)
	if f.GetOsVersion() == "" || f.GetRamBytes() == 0 || f.GetAgentBinaryHash() == "" || f.GetTimezone() == "" {
		t.Fatalf("a fact was not read on this machine: %v", f)
	}
	if len(f.GetAgentBinaryHash()) != 64 {
		t.Fatalf("binary hash %q is not a hex SHA-256", f.GetAgentBinaryHash())
	}
	t.Logf("facts: os_version=%q ram=%d tz=%q user=%q container=%v hash=%.12s...",
		f.GetOsVersion(), f.GetRamBytes(), f.GetTimezone(), f.GetUsername(), f.GetContainer(), f.GetAgentBinaryHash())
}

// launchd and systemd start the agent with a minimal PATH. tmux is then looked
// for in the usual install directories, after PATH.
func TestTmuxIsFoundInTheFallbackDirectoriesWhenPathLacksIt(t *testing.T) {
	pathWith(t, "") // no tmux on PATH
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\necho 'tmux 3.9'\n"), 0o700); err != nil { //nolint:gosec // G306: a test script must be executable.
		t.Fatal(err)
	}
	empty := t.TempDir()
	f, logs, _ := collectWith(t, machine.CollectorOptions{TmuxDirs: []string{empty, dir}})
	if f.GetTmuxVersion() != "tmux 3.9" {
		t.Fatalf("tmux_version = %q, want the one in the fallback directory", f.GetTmuxVersion())
	}
	if ev := logs.events("sample.failed"); len(ev) != 0 {
		t.Fatalf("a tmux found in a fallback directory logged a failure: %v", ev)
	}
}

// A file named tmux that is not executable is not tmux.
func TestANonExecutableTmuxInAFallbackDirectoryIsIgnored(t *testing.T) {
	pathWith(t, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\necho 'tmux 3.9'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, logs, _ := collectWith(t, machine.CollectorOptions{TmuxDirs: []string{dir}})
	if f.GetTmuxVersion() != "" || len(logs.events("sample.failed")) != 0 {
		t.Fatalf("tmux_version = %q, failures %v", f.GetTmuxVersion(), logs.events("sample.failed"))
	}
}

// PATH wins over the fallback directories.
func TestTmuxOnPathWinsOverTheFallbackDirectories(t *testing.T) {
	pathWith(t, `echo "tmux 1.1"`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\necho 'tmux 2.2'\n"), 0o700); err != nil { //nolint:gosec // G306: a test script must be executable.
		t.Fatal(err)
	}
	f, _, _ := collectWith(t, machine.CollectorOptions{TmuxDirs: []string{dir}})
	if f.GetTmuxVersion() != "tmux 1.1" {
		t.Fatalf("tmux_version = %q, want the one on PATH", f.GetTmuxVersion())
	}
}
