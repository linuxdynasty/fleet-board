package startgoal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameDirResolvesSymlinks(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if !sameDir(link, target) {
		t.Fatalf("sameDir(link, target) = false; want true after symlink resolution")
	}
	if !sameDir(target, link) {
		t.Fatalf("sameDir(target, link) = false; want true after symlink resolution")
	}
	if sameDir(link, t.TempDir()) {
		t.Fatalf("sameDir(link, other) = true; want false")
	}
	// Unresolvable sides fall back to a lexical compare.
	if !sameDir("/no/such/dir", "/no/such/dir") {
		t.Fatalf("sameDir of equal unresolvable paths = false; want true")
	}
	if sameDir("", "") || sameDir("", target) || sameDir(target, "") {
		t.Fatalf("sameDir with an empty side = true; want false")
	}
}
