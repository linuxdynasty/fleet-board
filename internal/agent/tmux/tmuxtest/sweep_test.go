package tmuxtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Sweep reads nothing but a pid from a folder: an owner file that names a
// program (planted by anyone who can write the temp folder) never runs it.
func TestSweepRunsNoProgramFromAnOwnerFile(t *testing.T) {
	b := anyBinary(t)
	dir, err := os.MkdirTemp(os.TempDir(), "fbt") //nolint:usetesting // Sweep looks for fbt* in the system temp folder itself
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "planted")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil { //nolint:gosec // G306: the planted program must be executable
		t.Fatal(err)
	}
	owner := fmt.Sprintf("%d\n%s\n", deadPid(t, b), script)
	if err := os.WriteFile(filepath.Join(dir, tmuxtest.OwnerFile), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxtest.Sweep()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Sweep ran the program an owner file named")
	}
}

// Sweep leaves a folder it cannot trust: a symlink named fbt* (it could
// point anywhere) or one other users may write. The server in it survives.
func TestSweepSkipsSymlinksAndLooseFolders(t *testing.T) {
	b := anyBinary(t)
	for _, tc := range []struct {
		name string
		make func(t *testing.T, target string) // makes the fbt* entry for target
	}{
		{"symlink", func(t *testing.T, target string) {
			t.Helper()
			link := filepath.Join(os.TempDir(), "fbtlink"+filepath.Base(target))
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(link) })
		}},
		{"group and world writable", func(t *testing.T, target string) {
			t.Helper()
			loose, err := os.MkdirTemp(os.TempDir(), "fbt") //nolint:usetesting // Sweep looks for fbt* in the system temp folder itself
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(loose) })
			if err := os.Chmod(loose, 0o777); err != nil { //nolint:gosec // G302: the untrusted folder under test
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(target, tmuxtest.OwnerFile), filepath.Join(loose, tmuxtest.OwnerFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(target, "s"), filepath.Join(loose, "s")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := os.MkdirTemp(os.TempDir(), "fbs") //nolint:usetesting // next to the fbt* entry; not fbt* itself, so only the entry under test is
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(target) })
			run := oneShot(t, b, target)
			if err := run("new-session", "-d", "-s", "kept", "exec cat"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = run("kill-server") })
			owner := fmt.Sprintf("%d\n", deadPid(t, b))
			if err := os.WriteFile(filepath.Join(target, tmuxtest.OwnerFile), []byte(owner), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.make(t, target)
			tmuxtest.Sweep()
			if err := run("has-session"); err != nil {
				t.Fatalf("Sweep killed the server behind an untrusted %s: %v", tc.name, err)
			}
		})
	}
}

func anyBinary(t *testing.T) tmuxtest.Binary {
	t.Helper()
	for _, b := range tmuxtest.Binaries() {
		if b.Path != "" {
			return b
		}
	}
	tmuxtest.Need(t, tmuxtest.Binaries()[1])
	return tmuxtest.Binary{}
}

// oneShot runs tmux commands against the socket dir/s.
func oneShot(t *testing.T, b tmuxtest.Binary, dir string) func(args ...string) error {
	t.Helper()
	r, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	return func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := r.Run(ctx, proc.Spec{Caller: c, Path: b.Path, Args: append([]string{"-S", filepath.Join(dir, "s"), "-f", "/dev/null"}, args...)})
		return err
	}
}
