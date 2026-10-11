package policy

import "path/filepath"

// Inspection is what one look at policy.yaml found: the file, its hash, and
// the policy or the problem. It is the one decision about what is on disk;
// [Load] and doctor both use it.
type Inspection struct {
	// Path is the policy file's path.
	Path string
	// Present is false when there is no file (a normal first run).
	Present bool
	// Hash is the sha256 (hex) of the file; empty when missing or unreadable.
	Hash string
	// SchemaVersion is the file's schema_version when it could be read.
	SchemaVersion string
	// Problem is why the file puts the agent in safe mode; nil when the file
	// is missing or valid.
	Problem *Problem
	// Policy is the parsed policy; an empty one (no local opinion) when the
	// file is missing or has a problem. Never nil.
	Policy *Policy
}

// Inspect reads policy.yaml in dir and judges it, with no side effect: no
// metric, log line or audit row, and nothing is created. A read that never
// blocks (see [readFile]). It is for doctor, which runs while the agent is
// down. Parse stats each allowed_repo_roots entry, so the answer is as of now.
func Inspect(dir string) Inspection {
	in, _ := inspectPath(filepath.Join(dir, File))
	return in
}

// inspectPath is Inspect for a full path. The error is the read error
// behind an unreadable problem, for the caller's log; it is not part of the
// judgement.
func inspectPath(path string) (Inspection, error) {
	fr, rerr := readFile(path)
	in := Inspection{Path: path, Present: fr.present, Hash: fr.hash, Policy: &Policy{}}
	switch {
	case fr.problem != nil:
		in.Problem = fr.problem
	case fr.present:
		res := Parse(fr.data)
		in.SchemaVersion = res.SchemaVersion
		if res.Problem != nil {
			in.Problem = res.Problem
		} else {
			in.Policy = res.Policy
		}
	}
	return in, rerr
}
