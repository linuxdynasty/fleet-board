package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// stateDirCheck is the state folder: it must be a real folder (not a link),
// owned by the user, with no group or other access, as `fleet-agent run`
// insists (conn.CheckDir).
func stateDirCheck(_ context.Context, e *Env) Outcome {
	dir := e.StateDir()
	p, err := lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fail(dir+" does not exist", "fleet-agent enroll --token <token> creates it; with another folder pass --state-dir")
	case err != nil:
		return unknown("cannot look at "+dir, "check that you may read its parent folder")
	case !p.IsDir:
		return fail(dir+" is not a folder (a link counts as not a folder)", "move it away and run fleet-agent enroll, or pass --state-dir with a real folder")
	case !e.owned(p):
		return fail(fmt.Sprintf("%s is owned by another user (uid %d, not uid %d)", dir, p.UID, e.opts.Getuid()),
			"use a folder you own with --state-dir, or fix the owner with chown")
	case p.perm()&0o077 != 0:
		return fail(fmt.Sprintf("%s is mode %s: other users could read the key in it", dir, modeText(p)), "chmod 700 "+shellQuote(dir))
	}
	return ok(fmt.Sprintf("%s is mode %s and yours", dir, modeText(p)))
}

// configDirCheck is the config folder. Reading by others is the owner's
// choice (policy.yaml is mode 0644); writing by others is not: anyone who can
// write there can loosen the machine's ceiling (ADR 0009).
func configDirCheck(_ context.Context, e *Env) Outcome {
	dir := e.ConfigDir()
	p, err := stat(dir) // a link to a folder is fine here: dotfile managers make them
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ok(dir + " does not exist yet; fleet-agent enroll or run creates it (mode 0700)")
	case err != nil:
		return unknown("cannot look at "+dir, "check that you may read its parent folder")
	case !p.IsDir:
		return fail(dir+" is not a folder", "move it away; fleet-agent run makes the folder, or pass --config-dir")
	case !e.owned(p):
		return fail(fmt.Sprintf("%s is owned by another user (uid %d, not uid %d)", dir, p.UID, e.opts.Getuid()),
			"use a folder you own with --config-dir, or fix the owner with chown")
	case p.perm()&0o022 != 0:
		return fail(fmt.Sprintf("%s is mode %s: other users could change policy.yaml and loosen this machine's ceiling", dir, modeText(p)),
			"chmod go-w "+shellQuote(dir))
	}
	return ok(fmt.Sprintf("%s is mode %s and yours", dir, modeText(p)))
}

// probeFolderCheck is the usage probe's folder (01 C3): it should exist, be
// private and stay empty. Names found in it are never printed.
func probeFolderCheck(_ context.Context, e *Env) Outcome {
	dir := e.opts.ProbeDir
	if dir == "" {
		return unknown("no probe folder is configured", "report this as a bug in fleet-agent doctor")
	}
	p, err := lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return warn(dir+" does not exist yet; the usage probe will need it", "mkdir -p -m 700 "+shellQuote(dir))
	case err != nil:
		return unknown("cannot look at "+dir, "check that you may read its parent folder")
	case !p.IsDir:
		return warn(dir+" is not a folder", "move it away and make the folder: mkdir -p -m 700 "+shellQuote(dir))
	case !e.owned(p) || p.perm()&0o077 != 0:
		return warn(fmt.Sprintf("%s is mode %s or not yours; it should be private", dir, modeText(p)), "chmod 700 "+shellQuote(dir))
	}
	f, err := os.Open(dir) //nolint:gosec // G304: the probe folder, opened only to see whether it is empty
	if err != nil {
		return unknown("cannot read "+dir, "check its permissions")
	}
	defer func() { _ = f.Close() }()
	if _, err := f.ReadDir(1); !errors.Is(err, io.EOF) {
		if err != nil {
			return unknown("cannot read "+dir, "check its permissions")
		}
		return warn(dir+" is not empty; the probe keeps it empty", "remove what is in it (the agent never puts files there)")
	}
	return ok(dir + " exists, is empty and is private")
}
