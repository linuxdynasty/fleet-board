package policy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// StarterDefaultsMarker is the line before the commented defaults.
const StarterDefaultsMarker = "# Built-in defaults. Remove the # in front of a line to change it."

// StarterResult says what [WriteStarter] did.
type StarterResult struct {
	// Written is true when it wrote the file; Existed when a policy.yaml
	// was already there (and was left alone).
	Written, Existed bool
	// EmptyRoots is true when the file it wrote has no repos folder.
	EmptyRoots bool
}

const starterHead = `# fleet-agent policy: this machine's ceiling. The hub can only tighten it
# and never writes this file. A change takes effect when fleet-agent restarts.
# Keys and ranges: docs/specs/03e-settings-and-drift.md.
schema_version: "1"

# Folders that hold your repos. With none listed the agent looks at no repo.
`

const starterDefaults = StarterDefaultsMarker + `
# default_server: control
# max_active: 2
# max_total: 4
# max_windows: 12
# allowed_harnesses: [claude, codex, muse, cursor, opencode]
# local_approval: []
# rate_limits:
#   - command_kind: SendKeys
#     per_minute: 60
# cert_lifetime: 2160h
# attach_idle_timeout: 30m
# idle_threshold: 2m
# spool_cap_bytes: 2147483648
# block_on_battery: false
# share_identity_email: false
# window_name_template: "{harness} {work}"
#
# Off unless you set them: min_probe_interval, labels, metrics_listen,
# pprof_listen, telemetry.
`

func starterContent(repos string) string {
	roots := "allowed_repo_roots: []\n"
	if repos != "" {
		roots = "allowed_repo_roots: [" + strconv.Quote(repos) + "]\n"
	}
	return starterHead + roots + "\n" + starterDefaults
}

// WriteStarter writes policy.yaml in dir (created 0700 if missing) with
// schema_version "1", allowed_repo_roots set to repos (empty when repos is
// ""), and every other key as a commented default. The file is mode 0644 and
// written all or nothing; an existing policy.yaml is never touched.
func WriteStarter(dir, repos string) (StarterResult, error) {
	if !printable(repos, 4096) {
		return StarterResult{}, errors.New("policy: the repos folder name has a control character")
	}
	if err := EnsureDir(dir); err != nil {
		return StarterResult{}, err
	}
	f, err := os.CreateTemp(dir, ".policy.yaml.tmp-*")
	if err != nil {
		return StarterResult{}, fmt.Errorf("policy: write starter file: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.WriteString(starterContent(repos)); err != nil {
		_ = f.Close()
		return StarterResult{}, fmt.Errorf("policy: write starter file: %w", err)
	}
	if err := errors.Join(f.Chmod(0o644), f.Sync(), f.Close()); err != nil {
		return StarterResult{}, fmt.Errorf("policy: write starter file: %w", err)
	}
	// A hard link fails when the name exists, so a file made by hand in the
	// meantime is never replaced.
	if err := os.Link(tmp, filepath.Join(dir, File)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return StarterResult{Existed: true}, nil
		}
		return StarterResult{}, fmt.Errorf("policy: write starter file: %w", err)
	}
	if d, err := os.Open(dir); err == nil { //nolint:gosec // G304: the config folder, opened to sync it
		_ = errors.Join(d.Sync(), d.Close())
	}
	return StarterResult{Written: true, EmptyRoots: repos == ""}, nil
}
