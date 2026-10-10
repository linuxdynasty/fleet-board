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
	// ErrNotReenrollable: the token names a previous machine. Re-enroll is
	// plan row 13a, which checks the machine's state; until then every
	// previous machine is refused (MACHINE_NOT_REENROLLABLE).
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
	// Actor is who asked, for the machine.enroll_token audit row; an empty
	// Actor.ID is Principal.
	Actor Actor
}

// InsertEnrollToken stores t in one writer transaction. Inside it, in
// order: an earlier row with the same principal and request_id is
// [ErrSecretDelivered] (same RequestHash) or [ErrRequestConflict]; a set
// PreviousMachine is [ErrNotReenrollable]; otherwise the row is inserted
// with its machine.enroll_token audit row (target enroll_tokens/<id>; the
// machine name, expected hostname and expiry, never the hash). Nothing is
// written on any error. The unique index on (principal, request_id) backs
// the check up.
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
		res, err := tx.ExecContext(jctx, `INSERT INTO enroll_tokens(secret_hash, machine_name, expected_hostname, labels,
			start_drained, not_after_time, created_time, principal, request_id, request_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.SecretHash[:], t.MachineName, t.ExpectedHostname, string(list), t.StartDrained,
			t.NotAfter.UnixMilli(), t.Created.UnixMilli(), t.Principal, t.RequestID, t.RequestHash[:])
		if err != nil {
			return fmt.Errorf("statedb: insert enroll token: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("statedb: enroll token id: %w", err)
		}
		return auditEnrollToken(jctx, tx, id, t)
	})
}

func auditEnrollToken(ctx context.Context, tx *sql.Tx, id int64, t EnrollToken) error {
	detail, err := auditDetail(struct {
		MachineName      string `json:"machine_name"`
		ExpectedHostname string `json:"expected_hostname"`
		NotAfter         string `json:"not_after"`
	}{t.MachineName, t.ExpectedHostname, t.NotAfter.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	actor := t.Actor
	if actor.ID == "" {
		actor.ID = t.Principal
	}
	_, err = AppendAudit(ctx, tx, AuditEntry{
		Actor: actor, Action: ActionEnrollToken, Target: fmt.Sprintf("enroll_tokens/%d", id),
		RequestID: t.RequestID, Detail: detail, Now: t.Created,
	})
	return err
}
