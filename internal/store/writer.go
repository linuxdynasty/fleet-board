package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ApplyFunc is one write job. It runs on the writer goroutine inside the
// batch transaction and must only use tx: no network call, no blocking wait,
// no Commit or Rollback (03f). Returning an error undoes this job's changes
// alone; the rest of the batch still commits.
type ApplyFunc func(tx *sql.Tx) error

type job struct {
	apply    ApplyFunc
	after    func()
	enqueued time.Time
	done     chan error // buffered: the writer never blocks on it
}

// Submit queues a job and returns a channel that receives its outcome once:
// nil after the batch holding it has committed durably, or the job's or the
// batch's error. Submit waits while the queue is full, until ctx ends. Jobs
// run in the order they were queued.
func (s *Store) Submit(ctx context.Context, apply ApplyFunc) (<-chan error, error) {
	return s.submit(ctx, apply, nil)
}

// SubmitHook queues a job like Submit, and registers a hook the writer runs
// after the job's batch commits durably, in commit order (#393): the hook
// publishes exactly this job's writes, so concurrent committers can never
// publish out of order. A failed job's hook never runs: a rolled-back job
// publishes nothing. The hook runs on the writer
// goroutine, so it must be short and non-blocking: read-pool reads and
// in-memory fan-out only, never another Submit or Write (the writer would
// wait on itself), and it must not fail. A panicking hook is recovered and
// logged; the writer lives on.
func (s *Store) SubmitHook(ctx context.Context, apply ApplyFunc, after func()) (<-chan error, error) {
	if after == nil {
		return nil, errors.New("store: nil hook")
	}
	return s.submit(ctx, apply, after)
}

func (s *Store) submit(ctx context.Context, apply ApplyFunc, after func()) (<-chan error, error) {
	if apply == nil {
		return nil, errors.New("store: nil job")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	j := job{apply: apply, after: after, enqueued: time.Now(), done: make(chan error, 1)}
	select {
	case s.jobs <- j:
		// This send may have filled the last slot: stamp the start of the
		// full period while the queue is full, so a full queue behind a
		// stuck writer reads even when no later Submit arrives. CAS keeps
		// the earliest stamp; a receive between the send and this check
		// means the queue already has room again, and then this is a no-op.
		if len(s.jobs) == cap(s.jobs) {
			s.fullSince.CompareAndSwap(0, time.Now().UnixNano())
		}
		return j.done, nil
	default:
	}
	// No room: this Submit found the queue full. Stamp the start of the full
	// period once; the writer clears it when it sees room again, so only an
	// unbroken full stretch counts.
	s.fullSince.CompareAndSwap(0, time.Now().UnixNano())
	select {
	case s.jobs <- j:
		return j.done, nil
	case <-ctx.Done():
		s.m.gaveUp(ctx)
		return nil, ctx.Err()
	}
}

func (s *Store) oldestJob() time.Time {
	if ns := s.oldest.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// QueueFullSince is when the writer queue became continuously full, the
// WRITER_QUEUE_FULL reading of 05b. It is zero when the queue is not full:
// no Submit has filled or found it full since the writer last saw room.
func (s *Store) QueueFullSince() time.Time {
	if ns := s.fullSince.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// OldestJobAge is how long the oldest unanswered job has waited at now,
// the MAIN reading of the WRITER_BATCH_LAG signal of 05b. It is 0 when no
// job waits. It reads the writer's atomic stamp, never a transaction.
func (s *Store) OldestJobAge(now time.Time) time.Duration {
	if t := s.oldestJob(); !t.IsZero() {
		return now.Sub(t)
	}
	return 0
}

// Write submits a job and waits for its outcome. A ctx error means the
// outcome is unknown: the job may still commit.
func (s *Store) Write(ctx context.Context, apply ApplyFunc) error {
	return s.write(ctx, apply, nil)
}

// WriteHook submits a job like Write, with a post-commit hook for
// SubmitHook's ordering. A ctx error means the outcome is unknown: the
// job may still commit, and then its hook runs.
func (s *Store) WriteHook(ctx context.Context, apply ApplyFunc, after func()) error {
	if after == nil {
		return errors.New("store: nil hook")
	}
	return s.write(ctx, apply, after)
}

func (s *Store) write(ctx context.Context, apply ApplyFunc, after func()) error {
	done, err := s.submit(ctx, apply, after)
	if err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runHook runs one post-commit hook on the writer goroutine. A panicking
// hook must never kill the single writer: it is recovered and logged,
// and the writer moves on to the next job.
func (s *Store) runHook(after func()) {
	defer func() {
		if r := recover(); r != nil {
			// The panic's Go type only, never its value: a hook
			// panics with a programming error, and the value could
			// carry row text.
			s.log.Error("store.hook_failed", "panic", fmt.Sprintf("%T", r))
		}
	}()
	after()
}

// OnCommit registers a hook the writer runs after each committed batch's
// own hooks, in commit order (#393). Only one hook fits: registering
// again replaces the first. It must follow the hook rules (short,
// non-blocking, never Submit or Write). Unset, commits run nothing extra.
func (s *Store) OnCommit(after func()) {
	if after == nil {
		return
	}
	s.hook.Store(after)
}

// run is the single writer: it collects jobs until the batch holds BatchMax
// of them or BatchWindow has passed since its first job, then commits the
// batch as one transaction. It exits after Close, once the queue is drained.
func (s *Store) run() {
	defer close(s.stopped)
	defer close(s.wake)
	timer := time.NewTimer(s.window)
	timer.Stop()
	var wait <-chan time.Time
	batch := make([]job, 0, s.max)
	for {
		select {
		case j, ok := <-s.jobs:
			if !ok {
				timer.Stop()
				s.flush(batch)
				return
			}
			batch = append(batch, j)
			// A receive frees one slot; a Submit blocked on the full queue
			// fills it again before this check runs, so room here means the
			// full period really broke.
			if len(s.jobs) < cap(s.jobs) {
				s.fullSince.Store(0)
			}
			if len(batch) == 1 {
				s.oldest.Store(j.enqueued.UnixNano())
				timer.Reset(s.window)
				wait = timer.C
			}
			if len(batch) < s.max {
				continue
			}
			timer.Stop()
		case <-wait:
		}
		wait = nil
		s.flush(batch)
		batch = batch[:0]
	}
}

// flush applies each job under its own savepoint and commits once. Every
// job's outcome is sent only after the commit returns, and then the
// successful jobs' hooks run in batch order (#393): commit order is
// publish order, decided by the writer, never by waiter timing.
func (s *Store) flush(batch []job) {
	if len(batch) == 0 {
		return
	}
	ctx := context.Background()
	start := time.Now()
	results := make([]error, len(batch))
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for i, j := range batch {
			jobErr, err := applyOne(ctx, tx, j.apply)
			if err != nil {
				return err
			}
			results[i] = jobErr
		}
		return nil
	})
	end := time.Now()
	if err != nil {
		s.log.Error("store.batch_failed", "jobs", len(batch), "err_kind", errKind(err), "sqlite_code", sqliteCode(err))
		for i := range results {
			results[i] = err
		}
	} else {
		for i, j := range batch {
			if j.after == nil || results[i] != nil {
				continue
			}
			s.runHook(j.after)
		}
		if h, ok := s.hook.Load().(func()); ok && h != nil {
			s.runHook(h)
		}
		// A hint needs no payload: the sole consumer re-reads committed state.
		// Coalescing keeps the writer independent of a slow or absent consumer.
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	s.m.batch(ctx, len(batch), end.Sub(start))
	// Cleared before any outcome is sent, so a caller holding its result never
	// scrapes the age of a job that was already answered.
	s.oldest.Store(0)
	for i, j := range batch {
		s.m.job(ctx, end.Sub(j.enqueued), results[i], err != nil)
		j.done <- results[i]
	}
}

// inTx runs fn in one BEGIN IMMEDIATE transaction and commits it.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback() // fails when a job already ended the transaction; err says why
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// applyOne runs one job between SAVEPOINT and RELEASE. jobErr is the job's
// own failure, already rolled back to the savepoint; err means the
// transaction itself is broken and the whole batch must fail.
func applyOne(ctx context.Context, tx *sql.Tx, apply ApplyFunc) (jobErr, err error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT job"); err != nil {
		return nil, fmt.Errorf("store: savepoint: %w", err)
	}
	if jobErr = apply(tx); jobErr != nil {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO job"); err != nil {
			return nil, fmt.Errorf("store: roll back a failed job: %w", errors.Join(err, jobErr))
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE job"); err != nil {
		return nil, fmt.Errorf("store: release savepoint: %w", err)
	}
	return jobErr, nil
}
