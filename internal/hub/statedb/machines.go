package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hostname"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// machinesSQL is migration 3 (plan row 3): machines, cert_serials and
// enroll_responses. cert_serials has no foreign key, so a revoked serial
// outlives DeleteMachine until its not_after (02g).
const machinesSQL = `CREATE TABLE machines(
	machine_id TEXT PRIMARY KEY CHECK (machine_id <> ''),
	name TEXT NOT NULL CHECK (name <> ''),
	expected_hostname TEXT NOT NULL CHECK (expected_hostname <> ''),
	labels TEXT NOT NULL DEFAULT '[]',
	drained INTEGER NOT NULL CHECK (drained IN (0, 1)),
	install_id TEXT NOT NULL CHECK (install_id <> ''),
	state TEXT NOT NULL CHECK (state IN ('ENROLLED', 'ONLINE', 'STALE', 'OFFLINE', 'REVOKED')),
	hostname_mismatch INTEGER NOT NULL CHECK (hostname_mismatch IN (0, 1)),
	facts TEXT NOT NULL DEFAULT '{}',
	created_time INTEGER NOT NULL
) STRICT;
CREATE TABLE cert_serials(
	serial TEXT PRIMARY KEY CHECK (serial <> '' AND serial = lower(serial)),
	machine_id TEXT NOT NULL,
	not_after_time INTEGER NOT NULL,
	revoked_time INTEGER,
	created_time INTEGER NOT NULL
) STRICT;
CREATE INDEX cert_serials_revoked ON cert_serials(not_after_time) WHERE revoked_time IS NOT NULL;
CREATE TABLE enroll_responses(
	id INTEGER PRIMARY KEY,
	secret_hash BLOB NOT NULL UNIQUE CHECK (length(secret_hash) = 32),
	install_id TEXT NOT NULL,
	csr_key_hash BLOB NOT NULL CHECK (length(csr_key_hash) = 32),
	machine_id TEXT NOT NULL REFERENCES machines(machine_id),
	cert BLOB NOT NULL,
	expire_time INTEGER NOT NULL,
	created_time INTEGER NOT NULL,
	UNIQUE (secret_hash, install_id, csr_key_hash)
) STRICT;`

// MachineEnrolled is the state of a machine that enrolled and never
// connected; row 3 writes no other.
const MachineEnrolled = "ENROLLED"

// ReplayWindow is how long after a token's not_after its stored Enroll
// response is still replayed (02g).
const ReplayWindow = 24 * time.Hour

// Enroll outcomes other than success (02g "Typed failures").
var (
	// ErrTokenUnknown: no enroll_tokens row has this secret hash.
	ErrTokenUnknown = errors.New("statedb: no such enroll token")
	// ErrTokenExpired: the token is unburned and past its not_after.
	ErrTokenExpired = errors.New("statedb: the enroll token has expired")
	// ErrTokenUsed: the token is burned and this is not a replay of the
	// response it gave (another install_id or CSR key, past the replay
	// window, or the machine is no longer live).
	ErrTokenUsed = errors.New("statedb: the enroll token was already used")
	// ErrPreviousMismatch: the request names a previous machine and the
	// token names a different one; the token is never burned here
	// (ENROLL_PREVIOUS_MACHINE_MISMATCH).
	ErrPreviousMismatch = errors.New("statedb: the request names a different previous machine than the token")
	// ErrPreviousGone: the token's previous machine was deleted after
	// the token was issued; the token's previous pointer is cleared
	// here, so Enroll treats it as a fresh machine.
	ErrPreviousGone = errors.New("statedb: the previous machine is gone; enroll fresh")
)

// EnrollKey is what keys an Enroll for replay: SHA-256 of the token secret,
// the agent's install_id, and SHA-256 of the CSR's SubjectPublicKeyInfo.
type EnrollKey struct {
	SecretHash [32]byte
	InstallID  string
	CSRKeyHash [32]byte
}

// Enrolled is the hub's part of an Enroll response, new or replayed. The
// rest of EnrollResponse comes from hub_identity.
type Enrolled struct {
	MachineID string
	CertDER   []byte
	// MachineName comes from the token; ExpectedHostname is the effective
	// one (the token's, or the machine name when the token has none).
	MachineName      string
	ExpectedHostname string
	HostnameMismatch bool
	Replayed         bool
}

// EnrollCheck is the outcome of [CheckEnroll] that is not an error: either
// a fresh token to enroll with (Token) or a stored response (Replay).
type EnrollCheck struct {
	Token  EnrollToken
	Replay *Enrolled
}

// querier is a read transaction or the writer's batch transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CheckEnroll is the read-pool pre-check (02g check order): an unknown
// token is [ErrTokenUnknown]; a burned one replays its stored response for
// the same key while the response is in its window and the machine is not
// REVOKED, and is [ErrTokenUsed] otherwise; an unburned token past its TTL
// is [ErrTokenExpired]. A token naming a previous machine returns it in
// [EnrollCheck.Token]; whether the machine is still eligible is decided
// again by [EnrollPrevious]. These facts never change once committed, so
// the answer holds; a fresh token is decided again by [Enroll].
func CheckEnroll(ctx context.Context, s *store.Store, key EnrollKey, now time.Time) (EnrollCheck, error) {
	var out EnrollCheck
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = classify(ctx, tx, key, now)
		return err
	})
	return out, err
}

func classify(ctx context.Context, q querier, key EnrollKey, now time.Time) (EnrollCheck, error) {
	var (
		t              EnrollToken
		labels         string
		notAfter       int64
		used           sql.NullInt64
		previous       sql.NullString
		secret, keyRaw = key.SecretHash[:], key.CSRKeyHash[:]
	)
	err := q.QueryRowContext(ctx, `SELECT machine_name, expected_hostname, labels, start_drained, not_after_time,
		used_time, previous_machine FROM enroll_tokens WHERE secret_hash = ?`, secret).
		Scan(&t.MachineName, &t.ExpectedHostname, &labels, &t.StartDrained, &notAfter, &used, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollCheck{}, ErrTokenUnknown
	}
	if err != nil {
		return EnrollCheck{}, fmt.Errorf("statedb: read enroll token: %w", err)
	}
	if used.Valid {
		r := &Enrolled{Replayed: true}
		err := q.QueryRowContext(ctx, `SELECT r.machine_id, r.cert, m.name, m.expected_hostname, m.hostname_mismatch
			FROM enroll_responses r JOIN machines m ON m.machine_id = r.machine_id
			WHERE r.secret_hash = ? AND r.install_id = ? AND r.csr_key_hash = ? AND r.expire_time > ? AND m.state <> 'REVOKED'`,
			secret, key.InstallID, keyRaw, now.UnixMilli()).
			Scan(&r.MachineID, &r.CertDER, &r.MachineName, &r.ExpectedHostname, &r.HostnameMismatch)
		if errors.Is(err, sql.ErrNoRows) {
			return EnrollCheck{}, ErrTokenUsed
		}
		if err != nil {
			return EnrollCheck{}, fmt.Errorf("statedb: read enroll response: %w", err)
		}
		return EnrollCheck{Replay: r}, nil
	}
	if notAfter <= now.UnixMilli() {
		return EnrollCheck{}, ErrTokenExpired
	}
	if err := json.Unmarshal([]byte(labels), &t.Labels); err != nil {
		return EnrollCheck{}, fmt.Errorf("statedb: enroll token labels: %w", err)
	}
	t.SecretHash = key.SecretHash
	t.NotAfter = time.UnixMilli(notAfter)
	if previous.Valid {
		t.PreviousMachine = previous.String
	}
	return EnrollCheck{Token: t}, nil
}

// NewMachine is one first Enroll: the key, the machine id and certificate
// the caller minted and signed before submitting, and the agent's facts.
type NewMachine struct {
	Key          EnrollKey
	MachineID    string
	CertDER      []byte
	Serial       string // ca.SerialHex of the certificate
	CertNotAfter time.Time
	// ReportedHostname is MachineFacts.hostname, compared with the token's
	// expected hostname.
	ReportedHostname string
	// Facts is MachineFacts as protojson, already within the request cap.
	Facts string
	Now   time.Time
}

// Enroll is one writer job that decides the burn: it marks the token used
// only while it is unburned, unexpired and names no previous machine, then
// inserts the machine (ENROLLED, version 1, stamped; a pending import
// entry for the name is consumed here with its labels merged in, the
// machine drained and its values as the machine's settings layer), its
// serial, the response kept until the token's not_after plus [ReplayWindow]
// and the machine.enroll audit row (no actor; the detail names the token
// row, whose own row names who issued it). A replay writes nothing. When the burn changes no row
// (another Enroll won, or the token expired since the pre-check), the job
// classifies again in the same transaction: the winner's response as a
// replay for the same key, or the [CheckEnroll] error. A ctx error means
// the outcome is unknown; a retry finds the replay.
// onCommit runs on the store writer after this job's batch commits, in
// commit order (#393): the Watch publish for exactly this enroll. It
// receives what the job stamped; a nil onCommit writes without one. It
// must follow the post-commit hook rules (short, non-blocking, never
// Submit or Write).
func Enroll(ctx context.Context, s *store.Store, m NewMachine, onCommit func(Enrolled)) (Enrolled, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out Enrolled
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(jctx, `UPDATE enroll_tokens SET used_time = ?
			WHERE secret_hash = ? AND used_time IS NULL AND not_after_time > ? AND previous_machine IS NULL`,
			m.Now.UnixMilli(), m.Key.SecretHash[:], m.Now.UnixMilli())
		if err != nil {
			return fmt.Errorf("statedb: burn enroll token: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return fmt.Errorf("statedb: burn enroll token: %w", err)
			}
			c, err := classify(jctx, tx, m.Key, m.Now)
			if err != nil {
				return err
			}
			if c.Replay == nil {
				return errors.New("statedb: the enroll token could not be burned")
			}
			out = *c.Replay
			return nil
		}
		out, err = insertMachine(jctx, tx, m)
		return err
	})
	if err != nil {
		return Enrolled{}, err
	}
	return out, nil
}

func insertMachine(ctx context.Context, tx *sql.Tx, m NewMachine) (Enrolled, error) {
	var (
		name, expected, labels string
		drained                bool
		tokenID, notAfter      int64
	)
	err := tx.QueryRowContext(ctx, `SELECT id, machine_name, expected_hostname, labels, start_drained, not_after_time
		FROM enroll_tokens WHERE secret_hash = ?`, m.Key.SecretHash[:]).Scan(&tokenID, &name, &expected, &labels, &drained, &notAfter)
	if err != nil {
		return Enrolled{}, fmt.Errorf("statedb: read burned enroll token: %w", err)
	}
	if expected == "" {
		expected = name
	}
	mismatch := hostname.Mismatch(expected, m.ReportedHostname)
	now := m.Now.UnixMilli()
	st, err := NextStamp(ctx, tx, m.Now)
	if err != nil {
		return Enrolled{}, err
	}
	// A pending import entry for the token's name is applied here, in
	// the same transaction (02m): its labels merge with the token's
	// and the machine is recorded drained until UndrainMachine. Its
	// values become the machine's settings layer. Import never drains
	// an enrolled machine, so only a pending entry drains here.
	pending, hasPending, err := consumePendingTx(ctx, tx, name)
	if err != nil {
		return Enrolled{}, err
	}
	if hasPending {
		merged, merr := MergeEnrollLabels(labels, pending.Labels)
		if merr != nil {
			return Enrolled{}, merr
		}
		labels, err = marshalLabels(merged)
		if err != nil {
			return Enrolled{}, err
		}
		drained = true
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, labels, drained,
		install_id, state, hostname_mismatch, facts, created_time, version, observed_time, hub_seq, update_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		m.MachineID, name, expected, labels, drained, m.Key.InstallID, MachineEnrolled, mismatch, m.Facts, now,
		st.ObservedTime.UnixMilli(), st.HubSeq, now); err != nil {
		return Enrolled{}, fmt.Errorf("statedb: insert machine: %w", err)
	}
	if hasPending && hasPendingValues(pending.Values) {
		if err := upsertMachineLayerTx(ctx, tx, m.MachineID, pending.Values, m.Now, st); err != nil {
			return Enrolled{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time)
		VALUES (?, ?, ?, ?)`, m.Serial, m.MachineID, m.CertNotAfter.UnixMilli(), now); err != nil {
		return Enrolled{}, fmt.Errorf("statedb: insert cert serial: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO enroll_responses(secret_hash, install_id, csr_key_hash, machine_id,
		cert, expire_time, created_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.Key.SecretHash[:], m.Key.InstallID, m.Key.CSRKeyHash[:], m.MachineID, m.CertDER,
		time.UnixMilli(notAfter).Add(ReplayWindow).UnixMilli(), now); err != nil {
		return Enrolled{}, fmt.Errorf("statedb: insert enroll response: %w", err)
	}
	detail, err := auditDetail(struct {
		MachineName      string `json:"machine_name"`
		ExpectedHostname string `json:"expected_hostname"`
		HostnameMismatch bool   `json:"hostname_mismatch"`
		EnrollToken      string `json:"enroll_token"`
	}{name, expected, mismatch, fmt.Sprintf("enroll_tokens/%d", tokenID)})
	if err != nil {
		return Enrolled{}, err
	}
	if _, err := AppendAudit(ctx, tx, AuditEntry{
		Action: ActionEnroll, Target: "machines/" + m.MachineID, MachineID: m.MachineID, Detail: detail, Now: m.Now,
	}); err != nil {
		return Enrolled{}, err
	}
	return Enrolled{
		MachineID: m.MachineID, CertDER: m.CertDER, MachineName: name, ExpectedHostname: expected, HostnameMismatch: mismatch,
	}, nil
}

// ReenrollMachine is one Enroll with a token naming a previous machine:
// the key, the previous id, the request's own previous hint, and the
// certificate the caller minted and signed for the previous id before
// submitting, with the agent's facts.
type ReenrollMachine struct {
	Key             EnrollKey
	PreviousMachine string
	// RequestPrevious is EnrollRequest.previous_machine_id: a hint that
	// matters only as a mismatch when it names another machine.
	RequestPrevious string
	CertDER         []byte
	Serial          string // ca.SerialHex of the certificate
	CertNotAfter    time.Time
	// ReportedHostname is MachineFacts.hostname, compared with the
	// row's expected hostname.
	ReportedHostname string
	// Facts is MachineFacts as protojson, already within the request cap.
	Facts string
	Now   time.Time
}

// EnrollPrevious re-enrolls the token's previous machine in one writer
// job (02g "Re-enroll"): a request hint naming another machine is
// [ErrPreviousMismatch] and burns nothing; a deleted previous machine
// clears the token's previous pointer and is [ErrPreviousGone], so the
// caller enrolls fresh; a previous machine that is neither REVOKED nor
// past its certificate lifetime is [ErrNotReenrollable] and burns
// nothing. Otherwise the token burns and the same machine id returns
// to ENROLLED with a new serial: the name, labels, expected hostname
// and drain stay (02g "Revoke, in one transaction": kept on the row),
// the install, facts and mismatch are refreshed, the version rises by
// 1 and the row is stamped, with its machine.enroll audit row. The
// revoked serials stay revoked. A ctx error means the outcome is
// unknown; a retry finds the replay.
func EnrollPrevious(ctx context.Context, s *store.Store, m ReenrollMachine, onCommit func(Enrolled)) (Enrolled, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var (
		out  Enrolled
		gone bool
	)
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		var (
			previous sql.NullString
			notAfter int64
		)
		err := tx.QueryRowContext(jctx, `SELECT previous_machine, not_after_time
			FROM enroll_tokens WHERE secret_hash = ?`, m.Key.SecretHash[:]).
			Scan(&previous, &notAfter)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTokenUnknown
		}
		if err != nil {
			return fmt.Errorf("statedb: read enroll token: %w", err)
		}
		if !previous.Valid {
			// Cleared after the machine was deleted: enroll fresh.
			return ErrPreviousGone
		}
		if previous.String != m.PreviousMachine {
			return ErrPreviousMismatch
		}
		if m.RequestPrevious != "" && m.RequestPrevious != previous.String {
			return ErrPreviousMismatch
		}
		var name, expected string
		err = tx.QueryRowContext(jctx, `SELECT name, expected_hostname FROM machines
			WHERE machine_id = ?`, previous.String).Scan(&name, &expected)
		if errors.Is(err, sql.ErrNoRows) {
			// The job commits the clear (a nil error), so the
			// caller enrolls fresh; an error would roll it back.
			if _, err := tx.ExecContext(jctx, `UPDATE enroll_tokens SET previous_machine = NULL
				WHERE secret_hash = ?`, m.Key.SecretHash[:]); err != nil {
				return fmt.Errorf("statedb: clear previous machine: %w", err)
			}
			gone = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("statedb: read previous machine: %w", err)
		}
		eligible, err := reenrollable(jctx, tx, previous.String, m.Now)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrNotReenrollable
		}
		res, err := tx.ExecContext(jctx, `UPDATE enroll_tokens SET used_time = ?
			WHERE secret_hash = ? AND used_time IS NULL AND not_after_time > ? AND previous_machine = ?`,
			m.Now.UnixMilli(), m.Key.SecretHash[:], m.Now.UnixMilli(), previous.String)
		if err != nil {
			return fmt.Errorf("statedb: burn enroll token: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return fmt.Errorf("statedb: burn enroll token: %w", err)
			}
			c, err := classify(jctx, tx, m.Key, m.Now)
			if err != nil {
				return err
			}
			if c.Replay == nil {
				return errors.New("statedb: the enroll token could not be burned")
			}
			out = *c.Replay
			return nil
		}
		var tokenID int64
		if err := tx.QueryRowContext(jctx, `SELECT id FROM enroll_tokens
			WHERE secret_hash = ?`, m.Key.SecretHash[:]).Scan(&tokenID); err != nil {
			return fmt.Errorf("statedb: read burned enroll token: %w", err)
		}
		mismatch := hostname.Mismatch(expected, m.ReportedHostname)
		nowMs := m.Now.UnixMilli()
		st, err := NextStamp(jctx, tx, m.Now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(jctx, `UPDATE machines SET state = ?, install_id = ?, facts = ?,
			hostname_mismatch = ?, version = version + 1, update_ms = ?, observed_time = ?, hub_seq = ?
			WHERE machine_id = ?`,
			MachineEnrolled, m.Key.InstallID, m.Facts, mismatch, nowMs,
			st.ObservedTime.UnixMilli(), st.HubSeq, previous.String); err != nil {
			return fmt.Errorf("statedb: re-enroll machine: %w", err)
		}
		if _, err := tx.ExecContext(jctx, `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time)
			VALUES (?, ?, ?, ?)`, m.Serial, previous.String, m.CertNotAfter.UnixMilli(), nowMs); err != nil {
			return fmt.Errorf("statedb: insert cert serial: %w", err)
		}
		if _, err := tx.ExecContext(jctx, `INSERT INTO enroll_responses(secret_hash, install_id, csr_key_hash, machine_id,
			cert, expire_time, created_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.Key.SecretHash[:], m.Key.InstallID, m.Key.CSRKeyHash[:], previous.String, m.CertDER,
			time.UnixMilli(notAfter).Add(ReplayWindow).UnixMilli(), nowMs); err != nil {
			return fmt.Errorf("statedb: insert enroll response: %w", err)
		}
		detail, err := auditDetail(struct {
			MachineName      string `json:"machine_name"`
			ExpectedHostname string `json:"expected_hostname"`
			HostnameMismatch bool   `json:"hostname_mismatch"`
			EnrollToken      string `json:"enroll_token"`
		}{name, expected, mismatch, fmt.Sprintf("enroll_tokens/%d", tokenID)})
		if err != nil {
			return err
		}
		if _, err := AppendAudit(jctx, tx, AuditEntry{
			Action: ActionEnroll, Target: "machines/" + previous.String,
			MachineID: previous.String, Detail: detail, Now: m.Now,
		}); err != nil {
			return err
		}
		out = Enrolled{
			MachineID: previous.String, CertDER: m.CertDER, MachineName: name,
			ExpectedHostname: expected, HostnameMismatch: mismatch,
		}
		return nil
	})
	if err != nil {
		return Enrolled{}, err
	}
	if gone {
		return Enrolled{}, ErrPreviousGone
	}
	return out, nil
}
