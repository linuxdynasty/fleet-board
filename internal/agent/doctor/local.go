package doctor

import (
	"context"
	"fmt"
)

// Local returns the checks that need no network and no running agent, in the
// order they are printed. Later rows add theirs by appending to this list in
// the command that calls [Run]: the hub handshake and Linux linger (plan row
// 6, PR 6h, listed here as not checked until then), the harness checks (row 8).
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
		{"push_socket", "Network push socket", pushSocketCheck},
		{"mdns_browse", "Last mDNS browse", mdnsBrowseCheck},
		{"snapshot_size", "Snapshot size", snapshotSizeCheck},
		{"clock", "Clock offset", clockCheck},
		{"enroll", "Last enroll", enrollCheck},
		{"tmux", "tmux", tmuxCheck},
		{"probe_folder", "Probe folder", probeFolderCheck},
		NotCheckedYetAt("hub_handshake", "Hub handshake", "row 6, PR 6h"),
		NotCheckedYetAt("linger", "Linux linger", "row 6, PR 6h"),
		NotCheckedYet("harness_paths", "Harness paths and versions", 8),
		NotCheckedYet("probe_trust", "Probe folder trust per harness", 8),
		NotCheckedYet("two_pane_folders", "Folders with two harness panes", 9),
	}
}

// NotCheckedYet is a check this build cannot run: it reports not_checked and
// names the plan row that adds it, so the list is whole and a missing check
// is never silent.
func NotCheckedYet(id, title string, row int) Check {
	return NotCheckedYetAt(id, title, fmt.Sprintf("row %d", row))
}

// NotCheckedYetAt is [NotCheckedYet] with a label that says more than the
// row, such as "row 6, PR 6h".
func NotCheckedYetAt(id, title, where string) Check {
	return Check{ID: id, Title: title, Run: func(context.Context, *Env) Outcome {
		return Outcome{Status: NotChecked, Reason: "this build cannot check it yet (" + where + ")"}
	}}
}
