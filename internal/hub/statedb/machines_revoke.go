package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// MachineSerial is one cert_serials row of a machine: the lowercase hex
// serial and its certificate's not_after.
type MachineSerial struct {
	Serial   string
	NotAfter time.Time
}

// RevokedCommand is one outbox row the revoke closed, for the caller to
// hand to the outbox service after commit.
type RevokedCommand struct {
	CommandID, MachineID, Kind, From string
	Created                          time.Time
}

// MachineRevoke is one RevokeMachine.
type MachineRevoke struct {
	MachineWrite
	MachineID       string
	ExpectedVersion uint64
	// Reason is shown in the audit log, up to 512 bytes.
	Reason string
	// CloseOpen closes the machine's open outbox rows inside the revoke
	// transaction (the caller passes outbox.CloseForRevoke mapped onto
	// [RevokedCommand]). A nil closer is an error: the revoke is one
	// transaction, and silently leaving commands open would send to a
	// revoked machine. It is a callback because the prompt release
	// needs the agent wire, which statedb never imports.
	CloseOpen func(ctx context.Context, tx *sql.Tx, machineID string, now time.Time) ([]RevokedCommand, error)
}

// RevokedMachine is the revoked row with its serials (for the
// revoked-serials file and the in-memory set, written before the job)
// and the outbox rows the job closed.
type RevokedMachine struct {
	Machine MachineStored
	Serials []MachineSerial
	Closed  []RevokedCommand
}

// MachineDelete is one DeleteMachine.
type MachineDelete struct {
	MachineWrite
	MachineID       string
	ExpectedVersion uint64
}

// machineSerials lists every certificate serial of a machine.
func machineSerials(ctx context.Context, tx *sql.Tx, machineID string) ([]MachineSerial, error) {
	rows, err := tx.QueryContext(ctx, `SELECT serial, not_after_time FROM cert_serials
		WHERE machine_id = ? ORDER BY serial`, machineID)
	if err != nil {
		return nil, fmt.Errorf("statedb: list machine serials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MachineSerial
	for rows.Next() {
		var s MachineSerial
		var end int64
		if err := rows.Scan(&s.Serial, &end); err != nil {
			return nil, fmt.Errorf("statedb: list machine serials: %w", err)
		}
		s.NotAfter = time.UnixMilli(end)
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokePlan is the non-mutating pre-check of one RevokeMachine: whether
// the request replays a stored response, and otherwise the machine's
// current serials to stage before the writer job. LiveRevoked is the
// current row's REVOKED state. A replay stages and closes nothing new:
// while the row is still REVOKED the caller re-cuts the live stream and
// re-stages the serials (healing a first attempt whose file write
// failed), but a replay after a re-enroll leaves the new serial and the
// new stream alone. A commit on an already REVOKED row is the idempotent
// already-revoked path. The writer job re-checks everything
// authoritatively, so a race between the pre-check and the commit fails
// closed (lines already staged stay revoked) and never misses a serial:
// the commit reconciles the serials the job itself revoked.
type RevokePlan struct {
	Replay      *MachineStored
	Serials     []MachineSerial
	LiveRevoked bool
}

// PrecheckRevoke decides a RevokeMachine on the read pool without
// writing: a replayed request_id returns the stored response, a missing
// row is [ErrMachineUnknown], a reused request_id for another request
// (another body, kind or machine) is [ErrRequestConflict], and a stale
// version is [VersionStale]. Otherwise the plan carries the serials to
// stage. An expired idempotency row reads as a miss here; the writer job
// drops it and prunes.
func PrecheckRevoke(ctx context.Context, s *store.Store, w MachineWrite, machineID string,
	expectedVersion uint64,
) (RevokePlan, error) {
	var plan RevokePlan
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		plan, err = precheckRevokeTx(ctx, tx, w, machineID, expectedVersion)
		return err
	})
	if err != nil {
		return RevokePlan{}, err
	}
	return plan, nil
}

// PrecheckRevokeForWrite runs the same non-mutating decision on the single
// writer, then invokes the request's bounded admission callback before the
// transaction ends. The callback must not read the store or perform I/O.
// Once Submit accepts the job, this waits for its definitive outcome even if
// the client disconnects; an allowance cannot be stranded by an unknown job.
func PrecheckRevokeForWrite(ctx context.Context, s *store.Store, w MachineWrite, machineID string,
	expectedVersion uint64,
) (RevokePlan, error) {
	var plan RevokePlan
	jctx := context.WithoutCancel(ctx)
	done, err := s.Submit(ctx, func(tx *sql.Tx) error {
		var err error
		plan, err = precheckRevokeTx(jctx, tx, w, machineID, expectedVersion)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return validatedWrite(jctx)
	})
	if err != nil {
		return RevokePlan{}, err
	}
	if err := <-done; err != nil {
		return RevokePlan{}, err
	}
	return plan, nil
}

func precheckRevokeTx(ctx context.Context, tx *sql.Tx, w MachineWrite, machineID string,
	expectedVersion uint64,
) (RevokePlan, error) {
	var plan RevokePlan
	row, err := readMachineLog(ctx, tx, w.Principal, w.RequestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return RevokePlan{}, fmt.Errorf("statedb: look up machine request_id: %w", err)
	case machineLogExpired(w.Now, row.createMs):
	case !bytes.Equal(row.hash, w.RequestHash[:]) || row.machine != machineID || row.kind != machineKindRevoke:
		return RevokePlan{}, ErrRequestConflict
	case row.deleted:
		// A revoke never stores a tombstone: the row belongs to a
		// delete under the same key, which is a different request.
		return RevokePlan{}, ErrRequestConflict
	default:
		stored, err := storedFromLogRow(row)
		if err != nil {
			return RevokePlan{}, err
		}
		rev, err := withRevocation(ctx, tx, stored)
		if err != nil {
			return RevokePlan{}, err
		}
		plan.Replay = &rev
	}
	got, err := readMachineRow(ctx, tx, machineID)
	if errors.Is(err, ErrMachineUnknown) {
		if plan.Replay != nil {
			// Revoked, then deleted: answer the stored response
			// with no effects.
			return plan, nil
		}
		return RevokePlan{}, err
	}
	if err != nil {
		return RevokePlan{}, err
	}
	serials, err := machineSerials(ctx, tx, machineID)
	if err != nil {
		return RevokePlan{}, err
	}
	plan.Serials = serials
	plan.LiveRevoked = got.State == MachineRevoked
	if plan.Replay != nil || plan.LiveRevoked {
		return plan, nil
	}
	return plan, checkVersion(got.Version, expectedVersion, "machine")
}

// RevokeMachine marks a machine REVOKED in one writer transaction: a
// replayed request_id returns the stored machine, a missing row is
// [ErrMachineUnknown], a stale version is [VersionStale]. A machine
// that is already REVOKED returns itself with no new audit row
// (idempotent). Otherwise the version rises by 1 and the row is
// stamped, every certificate serial of the machine is marked revoked,
// the closer shuts its open outbox rows (DONE / EXPIRED with
// MACHINE_REVOKED), and the machine.revoke audit row lands with the
// requester and reason. Goal, probe, session, log and triage effects
// land with the dispatch and attach rows that own them; Terminal and
// SessionLog are unimplemented, so closing the live stream is the
// caller's after-commit effect with Bye{REVOKED}.
func RevokeMachine(ctx context.Context, s *store.Store, in MachineRevoke) (RevokedMachine, error) {
	if in.CloseOpen == nil {
		return RevokedMachine{}, errors.New("statedb: revoke needs its outbox closer")
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out RevokedMachine
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if replay, err := checkMachineReplay(jctx, tx, in.MachineWrite, machineKindRevoke, in.MachineID); err != nil || replay != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				rev, rerr := withRevocation(jctx, tx, *replay)
				if rerr != nil {
					return rerr
				}
				serials, serr := machineSerials(jctx, tx, in.MachineID)
				if serr != nil {
					return serr
				}
				out = RevokedMachine{Machine: rev, Serials: serials}
			}
			return err
		}
		got, err := readMachineRow(jctx, tx, in.MachineID)
		if err != nil {
			return err
		}
		serials, err := machineSerials(jctx, tx, in.MachineID)
		if err != nil {
			return err
		}
		if got.State == MachineRevoked {
			rev, err := withRevocation(jctx, tx, got)
			if err != nil {
				return err
			}
			if err := validatedWrite(jctx); err != nil {
				return err
			}
			out = RevokedMachine{Machine: rev, Serials: serials}
			return logMachineRequest(jctx, tx, in.MachineWrite, machineKindRevoke, rev, false)
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "machine"); err != nil {
			return err
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE machines SET state = ?,
			version = version + 1, update_ms = ?, observed_time = ?, hub_seq = ? WHERE machine_id = ?`,
			MachineRevoked, nowMs, st.ObservedTime.UnixMilli(), st.HubSeq, in.MachineID); err != nil {
			return fmt.Errorf("statedb: revoke machine: %w", err)
		}
		if _, err := tx.ExecContext(jctx, `UPDATE cert_serials SET revoked_time = ?
			WHERE machine_id = ? AND revoked_time IS NULL`, nowMs, in.MachineID); err != nil {
			return fmt.Errorf("statedb: revoke machine serials: %w", err)
		}
		closed, err := in.CloseOpen(jctx, tx, in.MachineID, in.Now)
		if err != nil {
			return err
		}
		out = RevokedMachine{
			Machine: MachineStored{
				MachineID: got.MachineID, Name: got.Name, ExpectedHostname: got.ExpectedHostname,
				Labels: got.Labels, Drained: got.Drained, State: MachineRevoked, Version: got.Version + 1,
				CreateMs: got.CreateMs, UpdateMs: nowMs, HubSeq: st.HubSeq,
				Revoked: true, RevokeMs: nowMs,
				RevokeBy:     actorOrPrincipal(in.MachineWrite),
				RevokeReason: in.Reason,
			},
			Serials: serials, Closed: closed,
		}
		if err := auditMachine(jctx, tx, ActionMachineRevoke, out.Machine, in.MachineWrite,
			map[string]string{"reason": in.Reason}); err != nil {
			return err
		}
		return logMachineRequest(jctx, tx, in.MachineWrite, machineKindRevoke, out.Machine, false)
	})
	if err != nil {
		return RevokedMachine{}, err
	}
	return out, nil
}

// actorOrPrincipal is the audit actor of a machine write: the named
// actor, or the principal when the caller set none.
func actorOrPrincipal(w MachineWrite) Actor {
	if w.Actor.ID == "" {
		return Actor{ID: w.Principal, Kind: w.Actor.Kind, Name: w.Actor.Name}
	}
	return w.Actor
}

// DeleteMachine forgets a REVOKED machine in one writer transaction: a
// replayed request_id answers the same outcome, a missing row is
// [ErrMachineUnknown], a machine that is not REVOKED is
// [ErrMachineNotRevoked] and a stale version is [VersionStale]. The
// machines row goes with the rows that only make sense with it
// (connections, outbox commands, enroll responses, which a foreign key
// keeps otherwise); sessions, goals and logs keep their history under
// the machine id, the revoked serials stay until each certificate's
// not_after, and the audit log keeps everything, with the
// machine.delete row. After delete a token's previous_machine can no
// longer name the id: Enroll treats it as a fresh machine.
//
// The returned row keeps the last state with the delete's own Watch
// stamp (a resume orders the DELETED after every earlier change, as the
// repo delete does), and the full list row beside it for the Watch
// DELETED message; both carry Deleted.
func DeleteMachine(ctx context.Context, s *store.Store, in MachineDelete) (MachineStored, MachineRow, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out MachineStored
	var row MachineRow
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if replay, err := checkMachineReplay(jctx, tx, in.MachineWrite, machineKindDelete, in.MachineID); err != nil || replay != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				out = *replay
			}
			return err
		}
		got, err := readMachineRow(jctx, tx, in.MachineID)
		if err != nil {
			return err
		}
		if got.State != MachineRevoked {
			return ErrMachineNotRevoked
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "machine"); err != nil {
			return err
		}
		// The list row is read before the DELETE, in the same
		// transaction, so the DELETED carries the exact last state.
		row, err = scanMachineRow(tx.QueryRowContext(jctx, `SELECT `+machineColumns+` FROM machines WHERE machine_id = ?`,
			in.MachineID))
		if err != nil {
			return err
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		for _, q := range []struct {
			query, what string
		}{
			{`DELETE FROM machine_connections WHERE machine_id = ?`, "connections"},
			{`DELETE FROM commands WHERE machine_id = ?`, "commands"},
			{`DELETE FROM command_results WHERE machine_id = ?`, "command results"},
			{`DELETE FROM enroll_responses WHERE machine_id = ?`, "enroll responses"},
			{`DELETE FROM machines WHERE machine_id = ?`, "machine"},
		} {
			if _, err := tx.ExecContext(jctx, q.query, in.MachineID); err != nil {
				return fmt.Errorf("statedb: delete machine %s: %w", q.what, err)
			}
		}
		// The deleted row carries the delete's own position for
		// Watch, as the repo delete does.
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		out = got
		out.HubSeq, out.Deleted, out.Replay = st.HubSeq, true, false
		row.HubSeq = st.HubSeq
		if err := auditMachine(jctx, tx, ActionMachineDelete, MachineStored{
			MachineID: in.MachineID, Version: got.Version,
		}, in.MachineWrite, nil); err != nil {
			return err
		}
		return logMachineRequest(jctx, tx, in.MachineWrite, machineKindDelete, out, true)
	})
	if err != nil {
		return MachineStored{}, MachineRow{}, err
	}
	return out, row, nil
}
