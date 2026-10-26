package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// TestEnsureBoardTranslatesLane1Refusals is the row-20 coordinator seam:
// the lane-1 bootstrap's refusals surface as the goalfile errors the
// handlers map to 01d2 metadata, whatever the package that detected it.
func TestEnsureBoardTranslatesLane1Refusals(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "plain")
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureBoard(plain); err != nil {
		t.Fatalf("ensureBoard on a plain dir = %v, want nil", err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(link, ".fleet-board")); err != nil {
		t.Fatal(err)
	}
	err := mustBoardErr(t, link)
	if !errors.Is(err, goalfile.ErrDirNotPlain) || goalfile.Why(err) != "goal_dir_not_plain" {
		t.Fatalf("symlink board err = %v, want ErrDirNotPlain/goal_dir_not_plain", err)
	}

	foreign := filepath.Join(dir, "foreign")
	if err := os.MkdirAll(filepath.Join(foreign, ".fleet-board"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, ".fleet-board", ".gitignore"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = mustBoardErr(t, foreign)
	if !errors.Is(err, goalfile.ErrDirForeign) || goalfile.Why(err) != "goal_dir_foreign" {
		t.Fatalf("foreign board err = %v, want ErrDirForeign/goal_dir_foreign", err)
	}
}

func mustBoardErr(t *testing.T, workdir string) error {
	t.Helper()
	_, _, err := ensureBoard(workdir)
	if err == nil {
		t.Fatalf("ensureBoard(%s) = nil, want refusal", workdir)
	}
	return err
}
