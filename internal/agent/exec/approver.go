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

// approvedSuffix and deniedSuffix name a decision file: the command id plus
// one suffix. Command ids are 1 to 32 characters of [a-z0-9-] starting with
// [a-z0-9] (ids.proto), so a validated id is a safe file name.
const (
	approvedSuffix = ".approved"
	deniedSuffix   = ".denied"
)

// commandIDRE is the ids.proto shape every decision file name holds.
var commandIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ApprovalDir is the folder of pending local decisions for one state dir.
func ApprovalDir(stateDir string) string {
	return filepath.Join(stateDir, approvalsDirName)
}

// Decide records the person's answer for one waiting command: approved
// writes the approved file, denied the denied file. The create is atomic
// (O_EXCL): a second answer for the same command fails instead of
// overwriting the first. A decision for a command no wait asks about stays
// until a wait consumes it.
func Decide(stateDir, commandID string, approved bool) error {
	if !commandIDRE.MatchString(commandID) {
		return fmt.Errorf("exec: command id %q is not 1 to 32 characters of [a-z0-9-]", commandID)
	}
	if err := os.MkdirAll(ApprovalDir(stateDir), 0o700); err != nil {
		return fmt.Errorf("exec: make the approvals folder: %w", err)
	}
	for _, suffix := range []string{approvedSuffix, deniedSuffix} {
		if _, err := os.Stat(filepath.Join(ApprovalDir(stateDir), commandID+suffix)); err == nil {
			return fmt.Errorf("exec: %s is already decided", commandID)
		}
	}
	suffix := deniedSuffix
	if approved {
		suffix = approvedSuffix
	}
	path := filepath.Join(ApprovalDir(stateDir), commandID+suffix)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: commandID is the ids.proto shape, the state folder is the agent's own
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("exec: %s is already decided", commandID)
		}
		return fmt.Errorf("exec: record the decision: %w", err)
	}
	if err := f.Close(); err != nil {
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

// takeDecision consumes one pending decision file for the command id.
// found is false when nobody answered yet.
func takeDecision(dir, commandID string) (approved, found bool) {
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
		// O_EXCL create keeps a new answer from landing on it.
		_ = os.Remove(path)
		return tc.approved, true
	}
	return false, false
}
