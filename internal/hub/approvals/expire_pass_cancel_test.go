package approvals_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func TestExpirePassCanceledWhileCommitPendingReturnsNoIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, id := newExpirePassStore(t, store.Options{
		BatchMax: 2, BatchWindow: time.Minute,
	})

	passCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		ids []string
		err error
	}
	passDone := make(chan result, 1)
	go func() {
		ids, err := approvals.ExpirePass(passCtx, s, t0.Add(31*time.Minute))
		passDone <- result{ids, err}
	}()
	// The pass is the first job in a two-job batch. The second job holds the
	// commit after the pass has found its ID, so cancellation has a known point.
	deadline := time.After(5 * time.Second)
	for s.OldestJobAge(time.Now()) == 0 {
		select {
		case <-deadline:
			t.Fatal("expiry job was not picked up")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	blockDone, err := s.Submit(ctx, func(*sql.Tx) error {
		close(entered)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("commit blocker was not reached")
	}
	cancel()
	select {
	case got := <-passDone:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("ExpirePass error = %v, want context cancellation", got.err)
		}
		if len(got.ids) != 0 {
			t.Fatalf("ExpirePass IDs = %v before commit, want none", got.ids)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExpirePass did not return on cancellation")
	}
	releaseOnce()
	if err := <-blockDone; err != nil {
		t.Fatal(err)
	}
	if got := auditActions(t, s, "approvals/"+id); len(got) != 1 || got[0] != "approval.expire" {
		t.Fatalf("committed audit = %v, want one approval.expire", got)
	}
}

func TestExpirePassCanceledWhileQueuedReturnsNoIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newExpirePassStore(t, store.Options{BatchMax: 1, QueueSize: 1})
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	blockDone, err := s.Submit(ctx, func(*sql.Tx) error {
		close(entered)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocker was not reached")
	}
	passCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		ids []string
		err error
	}
	passDone := make(chan result, 1)
	go func() {
		ids, err := approvals.ExpirePass(passCtx, s, t0.Add(31*time.Minute))
		passDone <- result{ids, err}
	}()
	deadline := time.After(5 * time.Second)
	for s.QueueFullSince().IsZero() {
		select {
		case <-deadline:
			t.Fatal("expiry job was not queued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case got := <-passDone:
		if !errors.Is(got.err, context.Canceled) || len(got.ids) != 0 {
			t.Fatalf("ExpirePass = (%v, %v), want no IDs and cancellation", got.ids, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExpirePass did not return on cancellation")
	}
	releaseOnce()
	if err := <-blockDone; err != nil {
		t.Fatal(err)
	}
}

func TestExpirePassReturnsIDsOnlyAfterCommit(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := mustCreate(t, s, confirmIn())
	got, err := approvals.ExpirePass(context.Background(), s, t0.Add(31*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != id {
		t.Fatalf("expired IDs = %v, want [%s]", got, id)
	}
	if actions := auditActions(t, s, "approvals/"+id); len(actions) != 1 || actions[0] != "approval.expire" {
		t.Fatalf("committed audit = %v, want one approval.expire", actions)
	}
}

func TestExpirePassRollbackReturnsNoIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	id := mustCreate(t, s, confirmIn())
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_expiry_audit
			BEFORE INSERT ON audit_log WHEN NEW.action = 'approval.expire'
			BEGIN SELECT RAISE(ABORT, 'test rollback'); END`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := approvals.ExpirePass(ctx, s, t0.Add(31*time.Minute))
	if err == nil || len(got) != 0 {
		t.Fatalf("ExpirePass = (%v, %v), want no IDs and rollback error", got, err)
	}
	var state string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT state FROM approvals WHERE id = ?`, id).Scan(&state)
	})
	if err != nil || state != "PENDING" {
		t.Fatalf("approval after rollback = (%q, %v), want PENDING", state, err)
	}
}

func newExpirePassStore(t *testing.T, opts store.Options) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	initial, err := store.Open(ctx, store.Options{
		Path: path, Name: "state", Migrations: statedb.Migrations(), BatchMax: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := mustCreate(t, initial, confirmIn())
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Path, opts.Name, opts.Migrations, opts.MustExist = path, "state", statedb.Migrations(), true
	s, err := store.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, id
}
