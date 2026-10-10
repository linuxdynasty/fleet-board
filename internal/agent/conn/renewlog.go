package conn

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// fileError is a renewal step that failed on a file in the state folder:
// file is its path, check a closed name for what is wrong, and fix, when
// the owner must act, what to do. All three are logged.
type fileError struct {
	file, check, fix string
	err              error
}

func (e *fileError) Error() string { return e.err.Error() }

func (e *fileError) Unwrap() error { return e.err }

// keyFileError sorts a renew.key that cannot be read or written. A damaged
// or loose one blocks renewal until the owner acts (the binding ruling
// keeps it), so the log names it and the fix.
func keyFileError(path string, err error) *fileError {
	switch {
	case errors.Is(err, errDamagedKey):
		return &fileError{file: path, check: "damaged", fix: "remove " + path + " so the next renewal makes a new key", err: err}
	case errors.Is(err, ErrLooseMode):
		return &fileError{
			file: path, check: "loose_mode", err: err,
			fix: "chmod 600 " + path + " if it is a file of this user, else remove it so the next renewal makes a new key",
		}
	default:
		return &fileError{file: path, check: "unusable", err: err}
	}
}

// renewRun logs and counts one renewal's failure on its span.
type renewRun struct {
	l    *Loop
	span trace.Span
}

// callFailed logs and counts a RenewCert that got no certificate. A
// non-retryable reason from the hub is a refusal; anything else (a bare
// UNAVAILABLE, UNIMPLEMENTED from an older hub, a lost connection, the
// hub's own INTERNAL) is a failure. Both are tried again at the next check.
func (r *renewRun) callFailed(ctx context.Context, err error) {
	r.span.RecordError(err)
	r.span.SetStatus(codes.Error, "call")
	code := "none"
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		code = ce.Code().String()
		if ed := errorDetail(ce); ed != nil && !ed.GetRetryable() &&
			ed.GetReason() != typev1.ReasonCode_REASON_CODE_UNSPECIFIED && ed.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
			r.l.m.renewal(ctx, "refused")
			r.l.log.WarnContext(ctx, "renew.refused", "reason", reasonName(ed.GetReason()), "code", code)
			return
		}
	}
	r.l.m.renewal(ctx, "failed")
	r.l.log.WarnContext(ctx, "renew.failed", "step", "call", "err_kind", errKind(err), "code", code)
}

// failed logs and counts a renewal that failed at step; result is the
// metric's label (invalid for an answer that failed a check).
func (r *renewRun) failed(ctx context.Context, step, result string, err error) {
	r.span.RecordError(err)
	r.span.SetStatus(codes.Error, step)
	r.l.m.renewal(ctx, result)
	if pe, ok := errors.AsType[*pairError](err); ok {
		r.l.log.WarnContext(ctx, "renew.failed", "step", step, "err_kind", "parse", "check", pe.check)
		return
	}
	r.l.renewFileFailed(ctx, step, err)
}

// renewFileFailed logs renew.failed for step with the file, check and fix
// of a *fileError.
func (l *Loop) renewFileFailed(ctx context.Context, step string, err error) {
	args := []any{"step", step, "err_kind", errKind(err)}
	if fe, ok := errors.AsType[*fileError](err); ok {
		args = append(args, "file", fe.file, "check", fe.check)
		if fe.fix != "" {
			args = append(args, "fix", fe.fix)
		}
	}
	l.log.WarnContext(ctx, "renew.failed", args...)
}
