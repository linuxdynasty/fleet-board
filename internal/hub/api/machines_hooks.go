package api

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/hookreconcile"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// opMachineHooks is the fleet_hub_config_writes_total op for the
// EnsureMachineHooks button (02m): one counter, the op label telling it
// apart, so no new metric name lands in telemetry-docs.json. The audit
// rows carry the per-command detail.
const opMachineHooks = "machine_hooks"

// hookAuditAction is the audit action of an EnsureMachineHooks fan-out.
const hookAuditAction = "machine.ensure_hooks"

// hookQueued is one fanned-out command with its response operation.
type hookQueued struct {
	op        outbox.Op
	operation *hubv1.Operation
}

// EnsureMachineHooks is the owner's "reinstall and re-verify the goal
// hooks on this machine" button (row 20): it fans out to one
// EnsureGoalHooks per (harness, enabled repo checkout) the call
// expands to, through the hub's reconcile decision (never for a live
// goal's harness, never for ESHU repos). An unset harness expands over
// the known harnesses; an unset repo over every enabled watched repo.
// The hub sends repo short names (never machine paths); expected hook
// versions are lane 2's to gate, so the button installs and verifies
// what the agent has. Each command replays by its derived request id,
// so a retried call returns its operations without new rows.
func (f *Fleet) EnsureMachineHooks(ctx context.Context, req *connect.Request[hubv1.EnsureMachineHooksRequest],
) (*connect.Response[hubv1.EnsureMachineHooksResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.machineRefused(ctx, opMachineHooks, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("EnsureMachineHooks"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.machineRefused(ctx, opMachineHooks, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.machineRefused(ctx, opMachineHooks, actor, err)
	}
	id := m.GetMachine().GetValue()
	if err := checkMachineID("machine", id); err != nil {
		return nil, f.machineRefused(ctx, opMachineHooks, actor, err)
	}
	harnesses, err := hookHarnesses(m.GetHarness().GetName())
	if err != nil {
		return nil, f.machineRefused(ctx, opMachineHooks, actor, err)
	}
	var repoHost, repoPath string
	if ref := m.GetRepo(); ref != nil && (ref.GetHost() != "" || ref.GetPath() != "") {
		if ref.GetHost() == "" || ref.GetPath() == "" {
			return nil, f.machineRefused(ctx, opMachineHooks, actor, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"repo needs both host and path", nil, nil))
		}
		repoHost, repoPath = ref.GetHost(), ref.GetPath()
	}
	now := f.hubNow()
	var ops []*hubv1.Operation
	var queued []hookQueued
	var hookAudits []statedb.AuditStored
	var version uint64
	var anyCreated bool
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		row, err := statedb.ReadMachineRowTx(ctx, tx, id)
		if err != nil {
			return err
		}
		version = uint64(row.Version) //nolint:gosec // G115: versions start at 1 and only rise
		stored, err := statedb.ListHookRepos(ctx, tx)
		if err != nil {
			return err
		}
		live, err := statedb.LiveGoalHarnesses(ctx, tx, id)
		if err != nil {
			return err
		}
		force, err := hookForce(stored, harnesses, m.GetProfile(), repoHost, repoPath)
		if err != nil {
			return err
		}
		wants := hookreconcile.Decide(hookreconcile.Input{
			Harnesses: hookViews(harnesses), Repos: hookRepos(stored), Live: live, Force: force,
		})
		ops = ops[:0]
		for i, w := range wants {
			cmd := hookreconcile.CommandFor(w, "")
			cmd.RequestedBy = who
			q, err := f.enqueueHook(ctx, tx, who, actor, m.GetRequestId(), i, id, now, cmd)
			if err != nil {
				return err
			}
			ops = append(ops, q.operation)
			if q.op.Created {
				anyCreated = true
				queued = append(queued, q)
				if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
					Actor: actorOf(who), Action: hookAuditAction, Target: "machines/" + id,
					CommandID: q.op.CommandID, RequestID: m.GetRequestId(), Now: now,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}, &hookAudits), func() {
		for _, q := range queued {
			f.publishOperationWatch(q.op.HubSeq, q.operation)
		}
		f.publishAudits(hookAudits)
	})
	if submitErr != nil {
		return nil, f.machineWriteError(ctx, opMachineHooks, "hooks", actor, machineStoreError("hooks", submitErr))
	}
	if commitErr := <-doneCh; commitErr != nil {
		return nil, f.machineWriteError(ctx, opMachineHooks, "hooks", actor, machineStoreError("hooks", commitErr))
	}
	for _, q := range queued {
		if f.onCommandQueued != nil {
			f.onCommandQueued(q.op)
		}
	}
	f.machineWrote(ctx, opMachineHooks, actor, id, version, !anyCreated)
	return connect.NewResponse(&hubv1.EnsureMachineHooksResponse{Operations: ops}), nil
}

// hookHarnesses expands the request's harness: explicit, or every known
// harness when unset. Unknown names are refused: the hub never invents
// a harness the agent does not know.
func hookHarnesses(name string) ([]string, error) {
	if name == "" {
		return hookreconcile.KnownHarnesses(), nil
	}
	for _, known := range hookreconcile.KnownHarnesses() {
		if name == known {
			return []string{name}, nil
		}
	}
	return nil, apiError(connect.CodeInvalidArgument,
		typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		"unknown harness", nil, nil)
}

// hookViews lists harnesses with no reported state: the button installs
// to learn it. Everything here may launch by construction; the agent
// refuses what it cannot run.
func hookViews(names []string) []hookreconcile.Harness {
	out := make([]hookreconcile.Harness, 0, len(names))
	for _, name := range names {
		out = append(out, hookreconcile.Harness{Name: name, LaunchAllowed: true})
	}
	return out
}

// hookRepos converts stored repos to reconcile inputs.
func hookRepos(stored []statedb.HookRepo) []hookreconcile.Repo {
	out := make([]hookreconcile.Repo, 0, len(stored))
	for _, r := range stored {
		out = append(out, hookreconcile.Repo{Host: r.Host, Path: r.Path, Enabled: r.Enabled, Mode: r.Mode})
	}
	return out
}

// hookForce builds one forced want per (harness, enabled checkout):
// the button means now, so state rules and the resend throttle do not
// apply. An explicit repo must be watched and enabled; ESHU and live
// goals still guard inside Decide.
func hookForce(stored []statedb.HookRepo, harnesses []string, profile, repoHost, repoPath string) ([]hookreconcile.Forced, error) {
	var force []hookreconcile.Forced
	seen := map[hookreconcile.Key]bool{}
	for _, r := range stored {
		if !r.Enabled {
			continue
		}
		if repoHost != "" && (r.Host != repoHost || r.Path != repoPath) {
			continue
		}
		for _, h := range harnesses {
			key := hookreconcile.Key{Harness: h, Profile: profile, RepoHost: r.Host, RepoPath: r.Path}
			if seen[key] {
				continue
			}
			seen[key] = true
			force = append(force, hookreconcile.Forced{Key: key, Reason: "button"})
		}
	}
	if repoHost != "" && len(force) == 0 {
		watched := false
		for _, r := range stored {
			if r.Host == repoHost && r.Path == repoPath {
				watched = true
			}
		}
		if !watched {
			return nil, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
				"no watched repo has this host and path", nil, nil)
		}
	}
	return force, nil
}

// enqueueHook replays or enqueues one fan-out command under its derived
// request id, so a retried button returns its operations idempotently.
// A replayed command is returned without a new audit row, like sendOne.
func (f *Fleet) enqueueHook(ctx context.Context, tx *sql.Tx, who *typev1.Principal, actor, requestID string, index int,
	machine string, now time.Time, cmd *agentv1.Command,
) (hookQueued, error) {
	key := requestID + "#" + strconv.Itoa(index)
	sum, err := machineBodyHash(cmd)
	if err != nil {
		return hookQueued{}, err
	}
	if op, replay, err := checkRequest(ctx, tx, actor, key, sum[:]); err != nil || replay {
		if err != nil {
			return hookQueued{}, err
		}
		if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
			return hookQueued{}, err
		}
		return hookQueued{operation: op}, nil
	}
	op, err := outbox.Enqueue(ctx, tx, outbox.In{
		MachineID: machine, Command: cmd, RequestID: key, Now: now,
		Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
	})
	if err != nil {
		return hookQueued{}, mapOutboxError(err)
	}
	if err := logRequest(ctx, tx, actor, key, op.CommandID, sum[:], now); err != nil {
		return hookQueued{}, err
	}
	operation, err := operationFromOp(ctx, tx, op, machine, who, requestID, now)
	if err != nil {
		return hookQueued{}, err
	}
	return hookQueued{op: op, operation: operation}, nil
}
