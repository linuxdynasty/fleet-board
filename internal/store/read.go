package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Read runs fn in a read transaction on a query_only connection from the
// pool, then ends it. fn sees the last committed state and never waits for
// the writer. Keep it short: a long read keeps the WAL from checkpointing,
// and it must not span a network call or a stream to a browser (04, Q1).
//
// The lock is not held while fn runs, so fn may call Submit or Write.
func (s *Store) Read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.isClosed() {
		return ErrClosed
	}
	start := time.Now()
	conn, err := s.readers.Conn(ctx)
	s.m.readWait(ctx, time.Since(start))
	if err != nil {
		if s.isClosed() {
			return ErrClosed
		}
		return fmt.Errorf("store: reader: %w", err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("store: begin read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a read transaction has nothing to keep
	return fn(tx)
}

func (s *Store) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}
