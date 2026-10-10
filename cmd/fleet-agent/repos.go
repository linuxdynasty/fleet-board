package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// isTerminal reports whether r is a terminal a person types at: a character
// device that is not /dev/null.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

// chooseRepos finds the repos folder for the starter policy file, before any
// network call (a typo must not cost the owner a one-time token). The
// --repos flag wins; else a terminal is asked; else there is none and the
// starter file says so. A blank answer or the end of input skips.
func chooseRepos(flag string, interactive bool, in io.Reader, prompt io.Writer, home string) (string, error) {
	if flag != "" {
		dir, err := policy.CheckRepoFolder(flag, home)
		if err != nil {
			return "", fmt.Errorf("--repos: %w", err)
		}
		return dir, nil
	}
	if !interactive {
		return "", nil
	}
	rd := bufio.NewReader(in)
	for {
		_, _ = fmt.Fprint(prompt, "Folder that holds your repos (for example ~/repos): ")
		line, err := rd.ReadString('\n')
		more := err == nil // false on the last line, which has no newline
		if strings.TrimSpace(line) == "" {
			if !more {
				_, _ = fmt.Fprintln(prompt) // end of input: leave the cursor on a fresh line
			}
			return "", nil // Enter, or no more input: skip
		}
		dir, cerr := policy.CheckRepoFolder(line, home)
		if cerr == nil {
			return dir, nil
		}
		_, _ = fmt.Fprintf(prompt, "%v. Try again, or press Enter to skip.\n", cerr)
		if !more {
			return "", nil
		}
	}
}

// starterNotes writes the starter policy file after a successful enroll and
// says what it did on w. A failure here is a warning, not an enroll failure:
// the machine is enrolled, and the file can be made by hand.
func starterNotes(w io.Writer, cfgDir, repos string) {
	path := filepath.Join(cfgDir, policy.File)
	res, err := policy.WriteStarter(cfgDir, repos)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(w, "fleet-agent: warning: could not write %s: %v\n", path, err)
	case res.Existed:
		_, _ = fmt.Fprintf(w, "fleet-agent: kept your existing %s\n", path)
	case res.EmptyRoots:
		_, _ = fmt.Fprintf(w, "fleet-agent: wrote %s with no repos folder; the agent looks at no repo until you list one under allowed_repo_roots\n", path)
	default:
		_, _ = fmt.Fprintf(w, "fleet-agent: wrote %s; the agent may look at repos under %s\n", path, repos)
	}
}

// hasPolicy reports whether the config folder already holds a policy.yaml.
func hasPolicy(cfgDir string) bool {
	_, err := os.Lstat(filepath.Join(cfgDir, policy.File))
	return err == nil
}
