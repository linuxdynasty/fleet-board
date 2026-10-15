package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Repo write ops for fleet_hub_repo_writes_total.
const (
	opRepoCreate = "create"
	opRepoUpdate = "update"
	opRepoDelete = "delete"
)

func (f *Fleet) countRepo(ctx context.Context, op, outcome string) {
	f.repos.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op), attribute.String("outcome", outcome)))
}

// repoRefused counts and logs a repo caller error by its reason and returns
// it. Every refusal path of CreateRepo, UpdateRepo and DeleteRepo ends here,
// so operators can tell why repo writes fail.
func (f *Fleet) repoRefused(ctx context.Context, op, actor string, err error) error {
	f.countRepo(ctx, op, "refused")
	f.tel.Logger.InfoContext(ctx, "repo.write_refused",
		"reason", repoReason(err), "op", op, "actor", actor)
	return err
}

// repoFailed counts and logs the hub's own repo failure at step (a closed
// set: encode, decode, store) and returns INTERNAL without the cause's text.
func (f *Fleet) repoFailed(ctx context.Context, op, verb, actor, step string, cause error) error {
	f.countRepo(ctx, op, "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "repo.write_failed",
		"step", step, "err_kind", kind, "op", op, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not "+verb+" the repo", nil, nil)
}

// repoWrote counts and logs a repo write with the repo and its version: ok
// for a committed write, replayed for an idempotent retry. The audit row
// carries the full detail; this is the 3 AM signal.
func (f *Fleet) repoWrote(ctx context.Context, op, actor, repo string, version uint64, replayed bool) {
	outcome := "ok"
	if replayed {
		outcome = "replayed"
	}
	f.countRepo(ctx, op, outcome)
	f.tel.Logger.InfoContext(ctx, "repo.written",
		"op", op, "repo", repo, "version", version, "replayed", replayed, "actor", actor)
}

// repoReason is the ErrorDetail reason name of a Connect error, without the
// REASON_CODE_ prefix (a closed set: the reasons repo writes return).
func repoReason(err error) string {
	reason := "UNSPECIFIED"
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if ed, ok := v.(*typev1.ErrorDetail); ok {
					reason = strings.TrimPrefix(ed.GetReason().String(), "REASON_CODE_")
				}
			}
		}
	}
	return reason
}

// repoWriteError renders a repo writer-job failure: caller errors stay
// refused, the hub's own become failed. An already-INTERNAL error (a corrupt
// stored row) is counted failed and kept as is. verb names the op for the
// failure text ("watch", "update", "delete").
func (f *Fleet) repoWriteError(ctx context.Context, op, verb, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() != connect.CodeInternal {
		return f.repoRefused(ctx, op, actor, err)
	}
	if errors.As(err, &ce) {
		f.countRepo(ctx, op, "failed")
		f.tel.Logger.ErrorContext(ctx, "repo.write_failed",
			"step", "store", "err_kind", "io", "op", op, "actor", actor)
		return err
	}
	return f.repoFailed(ctx, op, verb, actor, "store", err)
}
