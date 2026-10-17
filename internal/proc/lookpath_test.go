package proc_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// writeExe writes an executable file named name in dir.
func writeExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // G306: the fixture must be executable for LookPath to find it
		t.Fatal(err)
	}
	return p
}

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	want := writeExe(t, dir, "claude")
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644); err != nil { //nolint:gosec // G306: the fixture must be non-executable for LookPath to skip it
		t.Fatal(err)
	}
	empty := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+empty)

	cases := []struct {
		name, prog, want string
		notFound         bool
	}{
		{name: "finds an executable on PATH", prog: "claude", want: want},
		{name: "skips a non-executable file", prog: "plain", notFound: true},
		{name: "misses a missing binary", prog: "no-such-harness", notFound: true},
		{name: "rejects an empty name", prog: "", notFound: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := proc.LookPath(tc.prog)
			if tc.notFound {
				if !errors.Is(err, proc.ErrNotFound) {
					t.Fatalf("LookPath(%q) = %q, %v; want ErrNotFound", tc.prog, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LookPath(%q): %v", tc.prog, err)
			}
			if got != tc.want {
				t.Errorf("LookPath(%q) = %q, want %q", tc.prog, got, tc.want)
			}
		})
	}
}

// A slash names a path directly: an executable there is returned, anything
// else is not found and no PATH entry is consulted.
func TestLookPathSlash(t *testing.T) {
	dir := t.TempDir()
	exe := writeExe(t, dir, "harness")
	if got, err := proc.LookPath(exe); err != nil || got != exe {
		t.Errorf("LookPath(%q) = %q, %v; want the path itself", exe, got, err)
	}
	missing := filepath.Join(dir, "absent")
	if _, err := proc.LookPath(missing); !errors.Is(err, proc.ErrNotFound) {
		t.Errorf("LookPath(%q) = %v; want ErrNotFound", missing, err)
	}
}
