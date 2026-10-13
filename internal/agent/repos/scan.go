package repos

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Discover lists every git checkout directly under the allowed roots: a
// folder holding a `.git` file or folder (a linked worktree's `.git` is a
// file). Discovery does not descend into a recorded checkout, so nested
// checkouts and dependency folders stay cheap; it skips node_modules
// anywhere. A missing root is skipped, never an error; only the folder
// paths are returned here, git details come from [Inspect].
func Discover(roots []string) ([]string, error) {
	var out []string
	for _, root := range roots {
		top, err := filepath.EvalSymlinks(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(top, func(path string, d fs.DirEntry, walkErr error) error {
			// Unreadable folders are skipped, never fatal: only a clean
			// read descends or records, and the single return sits below.
			if walkErr == nil && d.IsDir() {
				switch name := d.Name(); {
				case path != top && (name == "node_modules" || name == ".git"):
					return filepath.SkipDir
				case path != top && hasGitDir(path):
					out = append(out, path)
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// hasGitDir reports whether dir holds a `.git` file, folder or link (a
// linked worktree's `.git` is a file). Only the name is statted.
func hasGitDir(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
