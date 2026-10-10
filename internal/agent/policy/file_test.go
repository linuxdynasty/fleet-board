package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// within runs f and fails the test when it does not return in time: the
// regression is a read that blocks forever.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
}

func TestAFifoNamedPolicyYamlIsSafeModeAndNeverBlocksLoad(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	mkfifo(t, r.path)
	var m *policy.Manager
	within(t, "Load on a FIFO", func() { m = r.load(t) })
	if !m.SafeMode() || m.Info().GetError() != "the file cannot be read" || m.Info().GetHash() != "" {
		t.Errorf("Info = %v, want safe mode, unreadable, no hash", m.Info())
	}
}

func TestAFifoPutInPlaceOfTheFileNeverBlocksRecheckOrSnapshot(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, r.path)
	within(t, "Recheck on a FIFO", func() { m.Recheck(t.Context()) })
	within(t, "Snapshot on a FIFO", func() { _ = m.Info() })
	r.noChange(t, 50*time.Millisecond)
	if ev := r.events(t, "policy.check_failed"); len(ev) != 1 {
		t.Errorf("policy.check_failed = %v, want one", ev)
	}
}

func TestASymlinkToARegularFileIsFollowedAndOneToAnythingElseIsRefused(t *testing.T) {
	t.Parallel()
	// A dotfile manager links policy.yaml to a file in a repo.
	r := newRig(t, "")
	target := filepath.Join(t.TempDir(), "policy-real.yaml")
	if err := os.WriteFile(target, []byte(goodFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, r.path); err != nil {
		t.Fatal(err)
	}
	if m := r.load(t); m.SafeMode() || m.Info().GetHash() != sum(goodFile) {
		t.Errorf("Info = %v, want the linked file loaded", m.Info())
	}

	cases := map[string]func(t *testing.T) string{
		"a link to a FIFO": func(t *testing.T) string {
			t.Helper()
			p := filepath.Join(t.TempDir(), "fifo")
			mkfifo(t, p)
			return p
		},
		"a link to a folder": func(t *testing.T) string { t.Helper(); return t.TempDir() },
		"a link to nothing":  func(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "gone") },
	}
	for name, mk := range cases {
		r := newRig(t, "")
		if err := os.Symlink(mk(t), r.path); err != nil {
			t.Fatal(err)
		}
		var m *policy.Manager
		within(t, name, func() { m = r.load(t) })
		if !m.SafeMode() || m.Info().GetError() != "the file cannot be read" {
			t.Errorf("%s: Info = %v, want safe mode (unreadable)", name, m.Info())
		}
	}
}

// The cap is on the bytes read: exactly 64 KiB is read whole, one more byte is
// too large. The hash of a too-large file covers its first 64 KiB + 1 bytes.
func TestTheSizeCapIsExactlySixtyFourKiB(t *testing.T) {
	t.Parallel()
	pad := func(n int) string {
		head := "schema_version: \"1\"\n# "
		return head + strings.Repeat("x", n-len(head)-1) + "\n"
	}
	exact := pad(policy.MaxFileBytes)
	if len(exact) != policy.MaxFileBytes {
		t.Fatalf("test file is %d bytes", len(exact))
	}
	if m := newRig(t, exact).load(t); m.SafeMode() || m.Info().GetHash() != sum(exact) {
		t.Errorf("a file of exactly 64 KiB: Info = %v, want it loaded", m.Info())
	}
	over := pad(policy.MaxFileBytes + 1)
	r := newRig(t, over)
	m := r.load(t)
	if !m.SafeMode() || m.Info().GetError() != "the file is over 64 KiB" {
		t.Errorf("a file of 64 KiB + 1: Info = %v, want too large", m.Info())
	}
	if m.Info().GetHash() != sum(over) {
		t.Errorf("hash = %s, want the hash of the bytes read (the file here is only 1 byte over)", m.Info().GetHash())
	}
	// An edit inside the first 64 KiB + 1 bytes of a too-large file is seen.
	r.write(t, "# edited\n"+over)
	m.Recheck(t.Context())
	r.nextChange(t)
}
