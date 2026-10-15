package statedb

import (
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

// MachineSerials lists every certificate serial of a machine, for the
// caller to stage onto the revocation list before the revoke's store
// write (02g write order). A missing machine has none.
func MachineSerials(ctx context.Context, s *store.Store, machineID string) ([]MachineSerial, error) {
	var out []MachineSerial
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = machineSerials(ctx, tx, machineID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
			out = RevokedMachine{Machine: rev, Serials: serials}
			return logMachineRequest(jctx, tx, in.MachineWrite, machineKindRevoke, rev, false)
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "machine"); err != nil {
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
// replayed request_id answers the same empty outcome, a missing row is
// [ErrMachineUnknown], a machine that is not REVOKED is
// [ErrMachineNotRevoked] and a stale version is [VersionStale]. The
// machines row goes with the rows that only make sense with it
// (connections, outbox commands, enroll responses, which a foreign key
// keeps otherwise); sessions, goals and logs keep their history under
// the machine id, the revoked serials stay until each certificate's
// not_after, and the audit log keeps everything, with the
// machine.delete row. After delete a token's previous_machine can no
// longer name the id: Enroll treats it as a fresh machine.
func DeleteMachine(ctx context.Context, s *store.Store, in MachineDelete) (MachineStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out MachineStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if replay, err := checkMachineReplay(jctx, tx, in.MachineWrite, machineKindDelete, in.MachineID); err != nil || replay != nil {
			if err == nil {
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
		out = MachineStored{MachineID: in.MachineID, Replay: false, Deleted: true}
		if err := auditMachine(jctx, tx, ActionMachineDelete, MachineStored{
			MachineID: in.MachineID, Version: got.Version,
		}, in.MachineWrite, nil); err != nil {
			return err
		}
		return logMachineRequest(jctx, tx, in.MachineWrite, machineKindDelete, out, true)
	})
	if err != nil {
		return MachineStored{}, err
	}
	return out, nil
}
