package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// renewalsSQL is migration 4 (plan row 4): cert_renewals keeps the
// certificate each RenewCert signed, keyed by the machine and the CSR's key,
// so a retry returns it instead of signing again (02g "Certificate renewal
// (A4)"). Its serial is a cert_serials row; like cert_serials it has no
// foreign key to machines.
const renewalsSQL = `CREATE TABLE cert_renewals(
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	csr_key_hash BLOB NOT NULL CHECK (length(csr_key_hash) = 32),
	serial TEXT NOT NULL UNIQUE REFERENCES cert_serials(serial),
	cert BLOB NOT NULL,
	created_time INTEGER NOT NULL,
	PRIMARY KEY (machine_id, csr_key_hash)
) STRICT;`

// RenewCert refusals (02g "Certificate renewal (A4)").
var (
	// ErrMachineUnknown: no machine row for the certificate's machine id
	// (never enrolled, or deleted).
	ErrMachineUnknown = errors.New("statedb: no machine row for the certificate")
	// ErrMachineRevoked: the machine row is REVOKED.
	ErrMachineRevoked = errors.New("statedb: the machine is revoked")
	// ErrPeerSerialUnknown: the presented certificate's serial is not in
	// cert_serials for this machine, or it is revoked there.
	ErrPeerSerialUnknown = errors.New("statedb: the certificate serial is unknown or revoked")
)

// Renewal is one RenewCert: the machine id and serial of the verified peer
// certificate, SHA-256 of the CSR's SubjectPublicKeyInfo, and Sign, which
// signs the certificate for that key. Sign runs inside the writer job and
// only when no certificate is stored for the key, so it must be local CPU
// work only: no network call, no wait.
type Renewal struct {
	MachineID  string
	PeerSerial string
	CSRKeyHash [32]byte
	Now        time.Time
	Sign       func() (Signed, error)
}

// Signed is a certificate Sign made.
type Signed struct {
	CertDER  []byte
	Serial   string // ca.SerialHex of the certificate
	NotAfter time.Time
}

// Renewed is the answer to a Renewal, new or stored.
type Renewed struct {
	CertDER  []byte
	Serial   string
	Replayed bool
}

// Renew answers a Renewal. A read-pool pre-check refuses a missing or
// REVOKED machine ([ErrMachineUnknown], [ErrMachineRevoked]) and an unknown
// or revoked peer serial ([ErrPeerSerialUnknown]), and returns a stored
// certificate for the same machine and key as a replay. Otherwise one writer
// job checks the same again, then signs and inserts the serial and the
// renewal together; a renewal stored since the pre-check is returned as a
// replay, so one machine and key never get two serials. Sign's error is
// returned as is. A ctx error means the outcome is unknown; a retry finds
// the replay.
func Renew(ctx context.Context, s *store.Store, r Renewal) (Renewed, error) {
	var out *Renewed
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = checkRenewal(ctx, tx, r)
		return err
	})
	if err != nil {
		return Renewed{}, err
	}
	if out != nil {
		return *out, nil
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var got Renewed
	err = s.Write(ctx, func(tx *sql.Tx) error {
		stored, err := checkRenewal(jctx, tx, r)
		if err != nil || stored != nil {
			if stored != nil {
				got = *stored
			}
			return err
		}
		got, err = insertRenewal(jctx, tx, r)
		return err
	})
	if err != nil {
		return Renewed{}, err
	}
	return got, nil
}

// checkRenewal refuses a renewal the store does not allow, returns the
// stored certificate for the machine and key, or returns nil when one must
// be signed.
func checkRenewal(ctx context.Context, q querier, r Renewal) (*Renewed, error) {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM machines WHERE machine_id = ?`, r.MachineID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMachineUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("statedb: read machine: %w", err)
	}
	if state == "REVOKED" {
		return nil, ErrMachineRevoked
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM cert_serials WHERE serial = ? AND machine_id = ? AND revoked_time IS NULL`,
		r.PeerSerial, r.MachineID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPeerSerialUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("statedb: read peer serial: %w", err)
	}
	stored := &Renewed{Replayed: true}
	err = q.QueryRowContext(ctx, `SELECT cert, serial FROM cert_renewals WHERE machine_id = ? AND csr_key_hash = ?`,
		r.MachineID, r.CSRKeyHash[:]).Scan(&stored.CertDER, &stored.Serial)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("statedb: read stored renewal: %w", err)
	}
	return stored, nil
}

func insertRenewal(ctx context.Context, tx *sql.Tx, r Renewal) (Renewed, error) {
	c, err := r.Sign()
	if err != nil {
		return Renewed{}, err
	}
	now := r.Now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time)
		VALUES (?, ?, ?, ?)`, c.Serial, r.MachineID, c.NotAfter.UnixMilli(), now); err != nil {
		return Renewed{}, fmt.Errorf("statedb: insert cert serial: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cert_renewals(machine_id, csr_key_hash, serial, cert, created_time)
		VALUES (?, ?, ?, ?, ?)`, r.MachineID, r.CSRKeyHash[:], c.Serial, c.CertDER, now); err != nil {
		return Renewed{}, fmt.Errorf("statedb: insert cert renewal: %w", err)
	}
	return Renewed{CertDER: c.CertDER, Serial: c.Serial}, nil
}
