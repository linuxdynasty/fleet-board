package worktree

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const (
	// maxScratchFiles caps one EnsureScratch at 8 files (command_scratch.proto).
	maxScratchFiles = 8
	// maxScratchFileBytes caps one file at 512 KiB.
	maxScratchFileBytes = 512 * 1024
	// maxScratchTotalBytes caps all files of one command at 768 KiB, so
	// the command stays under the 1 MiB message cap (02e).
	maxScratchTotalBytes = 768 * 1024
	// maxCollectBytes caps a CollectScratch read at 64 KiB; 0 means the cap.
	maxCollectBytes = 65536
	// maxScratchRead bounds a single CollectScratch disk read. Reports are
	// small by construction; anything past this reads truncated.
	maxScratchRead = 4 << 20
)

// scratchDir resolves the goal's scratch directory: <state>/scratch/<goal>.
// The hub sends only the goal id (a closed shape), so the directory cannot
// wander. It does not create it: EnsureScratch creates, CollectScratch
// refuses a missing directory.
func (m *Manager) scratchDir(goal string) (string, *agentv1.CommandResult) {
	if !validateGoalID(goal) {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"goal id must match the 02e id format")
	}
	return filepath.Join(m.state(), "scratch", goal), nil
}

// EnsureScratch creates the goal's scratch directory and writes its input
// files into it (F7). Declarative: the same files again are OK, and files
// not named are left alone. Over the size caps is INVALID_ARGUMENT; at
// most 8 files. The folder gets the board bootstrap for the goal file.
func (m *Manager) EnsureScratch(ctx context.Context, cmd *agentv1.EnsureScratch) *agentv1.CommandResult {
	_ = ctx
	dir, bad := m.scratchDir(cmd.GetGoal().GetValue())
	if bad != nil {
		return bad
	}
	files := cmd.GetFiles()
	if len(files) > maxScratchFiles {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"at most 8 scratch files per command")
	}
	var total int
	for _, f := range files {
		if !validateScratchName(f.GetName()) {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"scratch file names match [a-z0-9][a-z0-9._-]{0,63} and end in .json or .md")
		}
		if len(f.GetContent()) > maxScratchFileBytes {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"a scratch file holds at most 512 KiB")
		}
		total += len(f.GetContent())
		if total > maxScratchTotalBytes {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"scratch files hold at most 768 KiB per command")
		}
	}
	created := false
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		created = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"cannot create the scratch directory")
	}
	// MkdirAll follows a pre-planted symlink, so re-check after: writes
	// below must stay inside the agent-private state dir (01d2 posture).
	if fi, err := os.Lstat(dir); err != nil || !fi.Mode().IsDir() {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"scratch directory is not a plain directory")
	}
	if _, _, err := EnsureBoardDir(dir); err != nil {
		return boardResult(err)
	}
	for _, f := range files {
		path := filepath.Join(dir, f.GetName())
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: the name passed the closed pattern above
		if err != nil {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				fmt.Sprintf("cannot write scratch file %s", f.GetName()))
		}
		_, werr := out.Write(f.GetContent())
		serr := out.Sync()
		cerr := out.Close()
		if werr != nil || serr != nil || cerr != nil {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
				fmt.Sprintf("cannot write scratch file %s", f.GetName()))
		}
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK, Reason: typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Message: fmt.Sprintf("scratch ready for goal %s", cmd.GetGoal().GetValue()),
		Detail:  &agentv1.CommandResult_EnsureScratch{EnsureScratch: &agentv1.EnsureScratchResult{Created: created}},
	}
}

// CollectScratch reads one regular file the goal wrote into its scratch
// directory (F7). A link is never followed (SCRATCH_MISSING); the text is
// UTF-8 with control characters stripped except newline and tab, and is
// cut at max_bytes. The result text is UNTRUSTED: a harness wrote it.
func (m *Manager) CollectScratch(ctx context.Context, cmd *agentv1.CollectScratch) *agentv1.CommandResult {
	_ = ctx
	dir, bad := m.scratchDir(cmd.GetGoal().GetValue())
	if bad != nil {
		return bad
	}
	if !validateScratchName(cmd.GetName()) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"scratch file names match [a-z0-9][a-z0-9._-]{0,63} and end in .json or .md")
	}
	limit := cmd.GetMaxBytes()
	if limit == 0 || limit > maxCollectBytes {
		limit = maxCollectBytes
	}
	// A pre-planted symlink would carry the open below out of the state
	// dir (O_NOFOLLOW guards only the final component), so the directory
	// itself must be plain first; a link reads as missing (never followed).
	if fi, err := os.Lstat(dir); err != nil || !fi.Mode().IsDir() {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SCRATCH_MISSING,
			"no such scratch file")
	}
	path := filepath.Join(dir, cmd.GetName())
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: the name passed the closed pattern above
	if err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SCRATCH_MISSING,
			"no such scratch file")
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SCRATCH_MISSING,
			"no such scratch file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxScratchRead))
	if err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"cannot read the scratch file")
	}
	text := sanitizeScratch(raw)
	truncated := fi.Size() > maxScratchRead
	if !truncated && int64(len(text)) > int64(limit) {
		text = cutUTF8(text, int(limit))
		truncated = true
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK, Reason: typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Message: fmt.Sprintf("collected scratch file %s", cmd.GetName()),
		Detail: &agentv1.CommandResult_CollectScratch{CollectScratch: &agentv1.CollectScratchResult{
			Text: text, Truncated: truncated, SizeBytes: uint64(max(fi.Size(), 0)),
		}},
	}
}

// sanitizeScratch makes harness-written bytes safe text: valid UTF-8 with
// control characters stripped, except newline and tab.
func sanitizeScratch(raw []byte) string {
	s := strings.ToValidUTF8(string(raw), "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// cutUTF8 cuts s to at most max bytes on a rune boundary.
func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
