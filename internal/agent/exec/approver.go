package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// This file is the machine side of L3 local approval (01f): the person at
// the machine answers an AWAITING_LOCAL_CONFIRM wait with
// `fleet-agent approve|deny <command_id>`. The CLI writes one empty file
// per decision into the state folder; the executor's wait reads and
// consumes it. Files are the channel because the CLI is a separate process
// from the run it answers, and the command id is the only rendezvous the
// two share.

// approvalsDirName is the folder of pending local decisions under the
// agent state folder.
const approvalsDirName = "approvals"

// approvedSuffix and deniedSuffix name the previous decision layout: the
// command id plus one suffix. Command ids are 1 to 32 characters of
// [a-z0-9-] starting with [a-z0-9] (ids.proto), so a validated id is a
// safe file name. Decide now writes one suffix-less claim file holding the
// verdict instead, so concurrent approve and deny races fail atomically on
// the same O_EXCL create; takeDecision still consumes the old suffix files
// so a decision written before the switch is not lost and no orphan stays.
const (
	approvedSuffix = ".approved"
	deniedSuffix   = ".denied"
)

// claimVerdicts are the only contents Decide writes into the claim file.
const (
	claimApproved = "approved"
	claimDenied   = "denied"
)

// commandIDRE is the ids.proto shape every decision file name holds.
var commandIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ApprovalDir is the folder of pending local decisions for one state dir.
func ApprovalDir(stateDir string) string {
	return filepath.Join(stateDir, approvalsDirName)
}

// Decide records the person's answer for one waiting command by creating
// one claim file named for the command id holding the verdict. The create
// is atomic (O_EXCL on the shared name): concurrent approve and deny races
// meet on the same file, so exactly one succeeds and the other fails
// already-decided instead of overwriting the first. A decision for a
// command no wait asks about stays until a wait consumes it.
func Decide(stateDir, commandID string, approved bool) error {
	if !commandIDRE.MatchString(commandID) {
		return fmt.Errorf("exec: command id %q is not 1 to 32 characters of [a-z0-9-]", commandID)
	}
	if err := os.MkdirAll(ApprovalDir(stateDir), 0o700); err != nil {
		return fmt.Errorf("exec: make the approvals folder: %w", err)
	}
	dir := ApprovalDir(stateDir)
	for _, suffix := range []string{approvedSuffix, deniedSuffix} {
		if _, err := os.Stat(filepath.Join(dir, commandID+suffix)); err == nil {
			return fmt.Errorf("exec: %s is already decided", commandID)
		}
	}
	verdict := claimDenied
	if approved {
		verdict = claimApproved
	}
	path := filepath.Join(dir, commandID)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: commandID is the ids.proto shape, the state folder is the agent's own
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("exec: %s is already decided", commandID)
		}
		return fmt.Errorf("exec: record the decision: %w", err)
	}
	if _, err := f.WriteString(verdict); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("exec: record the decision: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("exec: record the decision: %w", err)
	}
	return nil
}

// approvalPoll is how often a wait looks for the person's file. The
// envelope asks again at most every approvalCheck (run.go), so a decision
// lands within about this long.
const approvalPoll = time.Second

// FileApprover is the Approver that reads `fleet-agent approve|deny`
// files. The zero value waits nowhere: Dir is required.
type FileApprover struct {
	// Dir is the state folder the CLI writes to (ApprovalDir joins it).
	Dir string
}

// Await returns once the person decides the command (approved, channel
// "cli") or ctx ends. A consumed file is removed, so a second wait for the
// same command finds nothing. An error is no decision: the executor waits
// out the round and asks again.
func (f FileApprover) Await(ctx context.Context, commandID, _ string) (bool, string, error) {
	if f.Dir == "" || !commandIDRE.MatchString(commandID) {
		return false, "", errors.New("exec: no approvals folder, or a malformed command id")
	}
	dir := ApprovalDir(f.Dir)
	if approved, found := takeDecision(dir, commandID); found {
		return approved, "cli", nil
	}
	ticker := time.NewTicker(approvalPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-ticker.C:
			if approved, found := takeDecision(dir, commandID); found {
				return approved, "cli", nil
			}
		}
	}
}

// takeDecision consumes one pending decision for the command id. It reads
// the shared claim file first, then the previous suffix layout, and removes
// every decision file it finds for the id so no orphan (including the loser
// of the old two-file race) stays behind. found is false when nobody
// answered yet.
func takeDecision(dir, commandID string) (approved, found bool) {
	if raw, err := os.ReadFile(filepath.Join(dir, commandID)); err == nil { //nolint:gosec // G304: commandID is the ids.proto shape, the dir is the agent's own
		_ = os.Remove(filepath.Join(dir, commandID))
		_ = os.Remove(filepath.Join(dir, commandID+approvedSuffix))
		_ = os.Remove(filepath.Join(dir, commandID+deniedSuffix))
		return string(raw) == claimApproved, true
	}
	for _, tc := range []struct {
		suffix   string
		approved bool
	}{
		{approvedSuffix, true},
		{deniedSuffix, false},
	} {
		path := filepath.Join(dir, commandID+tc.suffix)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		// The decision is taken even when the remove fails: a second
		// wait that finds the leftover answers the same way, and the
		// O_EXCL create keeps a new answer from landing on it. Both
		// suffixes come out so the loser's file never orphans.
		_ = os.Remove(filepath.Join(dir, commandID+approvedSuffix))
		_ = os.Remove(filepath.Join(dir, commandID+deniedSuffix))
		return tc.approved, true
	}
	return false, false
}
