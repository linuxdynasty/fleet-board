package doctor

import (
	"context"
	"fmt"
)

// Local returns the checks that need no network and no running agent, in the
// order they are printed. Later rows add theirs by appending to this list in
// the command that calls [Run]: the hub handshake and Linux linger (plan row
// 6, PR 6h), the harness checks (row 8).
func Local() []Check {
	return []Check{
		{"state_dir", "State folder", stateDirCheck},
		{"config_dir", "Config folder", configDirCheck},
		{"identity", "Identity", identityCheck},
		{"cert_expiry", "Certificate expiry", certExpiryCheck},
		{"cert_file", "Renewed certificate file", certFileCheck},
		{"renew_key", "Renewal key file", renewKeyCheck},
		{"policy_file", "Policy file", policyFileCheck},
		{"policy_roots", "Allowed repo folders", policyRootsCheck},
		{"policy_harnesses", "Allowed harnesses", policyHarnessesCheck},
		{"policy_watch", "Policy file watch", policyWatchCheck},
		{"status_file", "Status file", statusFileCheck},
		{"connection", "Hub connection", connectionCheck},
		{"snapshot_size", "Snapshot size", snapshotSizeCheck},
		{"clock", "Clock offset", clockCheck},
		{"enroll", "Last enroll", enrollCheck},
		{"tmux", "tmux", tmuxCheck},
		{"probe_folder", "Probe folder", probeFolderCheck},
		NotCheckedYet("harness_paths", "Harness paths and versions", 8),
		NotCheckedYet("probe_trust", "Probe folder trust per harness", 8),
		NotCheckedYet("two_pane_folders", "Folders with two harness panes", 9),
	}
}

// NotCheckedYet is a check this build cannot run: it reports not_checked and
// names the plan row that adds it, so the list is whole and a missing check
// is never silent.
func NotCheckedYet(id, title string, row int) Check {
	return Check{ID: id, Title: title, Run: func(context.Context, *Env) Outcome {
		return Outcome{Status: NotChecked, Reason: fmt.Sprintf("this build cannot check it yet (row %d)", row)}
	}}
}
