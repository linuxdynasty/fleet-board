package statusfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// EnrollFile is the name of the last enroll failure's file in the state
// folder. Its one writer is `fleet-agent enroll`.
const EnrollFile = "enroll.json"

// The closed codes of a failed enroll.
var enrollCodes = setOf(
	"refused", "hub_untrusted", "bad_response", "hub_failed", "already_enrolled", "loose_mode",
	"token_malformed", "no_hub", "hub_address", "no_hub_found", "timeout", "interrupted", "other",
)

// Enroll is the content of enroll.json: the code of the last failed
// enroll, and nothing else. It holds no hub address, token, message or
// path, so it is safe to print.
type Enroll struct {
	Version     int       `json:"version"`
	WrittenTime time.Time `json:"written_time"`
	// Code is one of a closed set ("refused", "hub_untrusted", "timeout", ...).
	Code string `json:"code"`
	// Reason is the hub's reason code (TOKEN_EXPIRED, ...) when Code is
	// "refused"; it comes from the contract's enum, never from a message.
	Reason string `json:"reason,omitempty"`
}

// EnrollCodeOf sorts the error of a failed enroll into its code (and, for
// a hub refusal, the reason name). It looks at the error's identity only,
// never at its text, which can echo what the owner typed.
func EnrollCodeOf(err error) (code, reason string) {
	var re *conn.RefusedError
	switch {
	case errors.As(err, &re):
		return "refused", strings.TrimPrefix(re.Reason.String(), "REASON_CODE_")
	case errors.Is(err, conn.ErrHubUntrusted):
		return "hub_untrusted", ""
	case errors.Is(err, conn.ErrBadResponse):
		return "bad_response", ""
	case errors.Is(err, conn.ErrHubFailed):
		return "hub_failed", ""
	case errors.Is(err, conn.ErrAlreadyEnrolled):
		return "already_enrolled", ""
	case errors.Is(err, conn.ErrLooseMode):
		return "loose_mode", ""
	case errors.Is(err, conn.ErrTokenMalformed):
		return "token_malformed", ""
	case errors.Is(err, conn.ErrNoHub):
		return "no_hub", ""
	case errors.Is(err, conn.ErrHubAddress):
		return "hub_address", ""
	case errors.Is(err, conn.ErrNoHubFound):
		return "no_hub_found", ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", ""
	case errors.Is(err, context.Canceled):
		return "interrupted", ""
	default:
		return "other", ""
	}
}

// RecordEnroll records the outcome of an enroll in dir.
// A failure replaces enroll.json with its code (whole or not at all); a
// success (enrollErr nil) removes the file, so it only ever shows the last
// failure and never one the owner has since fixed. A file that cannot be
// written or removed is logged (status.write_failed) and counted, and
// returned for the caller to ignore: the enroll's own result stands.
func RecordEnroll(tel *telemetry.Telemetry, dir string, now time.Time, enrollErr error) error {
	m, err := newMetrics(tel)
	if err != nil {
		return err
	}
	rec := m.recorder(EnrollFile, func() time.Time { return now })
	if enrollErr == nil {
		if err := remove(dir, EnrollFile); err != nil {
			rec.done(stepErr(stepRemove, err))
			return err
		}
		return nil
	}
	// A failure before the state folder exists still gets recorded; the
	// folder is made (0700) or, when it is open to other users, refused.
	if err := conn.EnsureDir(dir); err != nil {
		rec.done(stepErr(stepDir, err))
		return err
	}
	code, reason := EnrollCodeOf(enrollErr)
	data, err := encode(Enroll{Version: layout, WrittenTime: now.UTC().Truncate(time.Millisecond), Code: code, Reason: reason})
	if err == nil {
		err = writeAtomic(dir, EnrollFile, data, os.Rename)
	}
	rec.done(err)
	return err
}

// ReadEnroll reads enroll.json from dir. A missing file is [fs.ErrNotExist]
// (no failure is recorded: the last enroll worked, or none ran); unknown
// keys are ignored and every string is cleaned.
func ReadEnroll(dir string) (Enroll, error) {
	var e Enroll
	if err := readJSON(dir, EnrollFile, &e); err != nil {
		return Enroll{}, err
	}
	if e.Version != layout {
		return Enroll{}, fmt.Errorf("%w: %s has version %d, this build reads %d", ErrVersion, EnrollFile, e.Version, layout)
	}
	if e.WrittenTime.IsZero() {
		return Enroll{}, fmt.Errorf("%w: %s has no written_time", ErrDamaged, EnrollFile)
	}
	e.Code = enum(e.Code, enrollCodes)
	e.Reason = reasonName(e.Reason)
	return e, nil
}

// reasonName keeps an upper-case reason name (A-Z, 0-9, underscore, at
// most 48 bytes) and drops anything else.
func reasonName(s string) string {
	if len(s) > 48 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
		return StateUnknown
	}
	return s
}
