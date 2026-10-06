package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Outcomes of [InsertEnrollToken] other than success (02b "Idempotency").
var (
	// ErrSecretDelivered: the principal already used this request_id for the
	// same request; the token was shown then and is never stored, so the
	// caller makes a new one (SECRET_ALREADY_DELIVERED).
	ErrSecretDelivered = errors.New("statedb: this request_id already delivered its token")
	// ErrRequestConflict: the principal used this request_id for a different
	// request (REQUEST_ID_CONFLICT).
	ErrRequestConflict = errors.New("statedb: this request_id was used for a different request")
	// ErrNotReenrollable: the token names a previous machine. Plan row 2b
	// has no machines table, so every previous machine is refused
	// (MACHINE_NOT_REENROLLABLE); row 3 checks the machine's state instead.
	ErrNotReenrollable = errors.New("statedb: the previous machine cannot be re-enrolled")
)

// EnrollToken is one enroll_tokens row. It holds the SHA-256 of the
// token's secret, never the secret or the token text.
type EnrollToken struct {
	SecretHash  [32]byte
	MachineName string
	// ExpectedHostname is stored as asked; empty means MachineName.
	ExpectedHostname string
	Labels           []string
	StartDrained     bool
	NotAfter         time.Time
	// PreviousMachine is the machine id to re-enroll, or empty.
	PreviousMachine string
	Created         time.Time
	// Principal and RequestID key the idempotency record; RequestHash is
	// SHA-256 of the request body, to tell a retry from a conflict.
	Principal   string
	RequestID   string
	RequestHash [32]byte
}

// InsertEnrollToken stores t in one writer transaction. Inside it, in
// order: an earlier row with the same principal and request_id is
// [ErrSecretDelivered] (same RequestHash) or [ErrRequestConflict]; a set
// PreviousMachine is [ErrNotReenrollable]; otherwise the row is inserted.
// Nothing is written on any error. The unique index on (principal,
// request_id) backs the check up.
func InsertEnrollToken(ctx context.Context, s *store.Store, t EnrollToken) error {
	labels := t.Labels
	if labels == nil {
		labels = []string{}
	}
	list, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("statedb: token labels: %w", err)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	return s.Write(ctx, func(tx *sql.Tx) error {
		var prior []byte
		err := tx.QueryRowContext(jctx, `SELECT request_hash FROM enroll_tokens WHERE principal = ? AND request_id = ?`,
			t.Principal, t.RequestID).Scan(&prior)
		switch {
		case err == nil && bytes.Equal(prior, t.RequestHash[:]):
			return ErrSecretDelivered
		case err == nil:
			return ErrRequestConflict
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("statedb: look up request_id: %w", err)
		}
		if t.PreviousMachine != "" {
			return ErrNotReenrollable
		}
		_, err = tx.ExecContext(jctx, `INSERT INTO enroll_tokens(secret_hash, machine_name, expected_hostname, labels,
			start_drained, not_after_time, created_time, principal, request_id, request_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.SecretHash[:], t.MachineName, t.ExpectedHostname, string(list), t.StartDrained,
			t.NotAfter.UnixMilli(), t.Created.UnixMilli(), t.Principal, t.RequestID, t.RequestHash[:])
		if err != nil {
			return fmt.Errorf("statedb: insert enroll token: %w", err)
		}
		return nil
	})
}
