package store_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

func TestCommitWakeShowsCommittedStateBeforeJobOutcome(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchMax = 1
	s := h.open(t, opts)
	done, err := s.Submit(context.Background(), insert("saved"))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForWake(t, s.CommitWake())
	if got := rawValues(t, h.path); !slices.Equal(got, []string{"saved"}) {
		t.Fatalf("rows after wake = %v, want [saved]", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("job outcome: %v", err)
	}
}

func TestCommitWakeOnlyFollowsSuccessfulOuterCommit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchMax = 3
	s := h.open(t, opts)
	ctx := context.Background()
	var outcomes []<-chan error
	for _, apply := range []store.ApplyFunc{
		insert("rolled back"),
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "ROLLBACK")
			return err
		},
		insert("also rolled back"),
	} {
		done, err := s.Submit(ctx, apply)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		outcomes = append(outcomes, done)
	}
	// All three submissions fill the batch. An outcome proves flush finished.
	// Wait for all outcomes before checking that no wake escaped the failure.
	for i, done := range outcomes {
		if err := <-done; err == nil {
			t.Errorf("job %d: broken outer transaction returned nil error", i)
		}
	}
	if got := rawValues(t, h.path); len(got) != 0 {
		t.Fatalf("rows after failed batch = %v, want none", got)
	}
	select {
	case <-s.CommitWake():
		t.Fatal("failed outer transaction sent a wake")
	default:
	}
}

func TestCommitWakeIncludesSurvivingSavepointJobs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchMax = 3
	s := h.open(t, opts)
	jobs := []store.ApplyFunc{
		insert("first"),
		func(tx *sql.Tx) error {
			if err := insert("discarded")(tx); err != nil {
				return err
			}
			return errors.New("discard this job")
		},
		insert("last"),
	}
	var outcomes []<-chan error
	for _, apply := range jobs {
		done, err := s.Submit(context.Background(), apply)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		outcomes = append(outcomes, done)
	}
	waitForWake(t, s.CommitWake())
	if got := rawValues(t, h.path); !slices.Equal(got, []string{"first", "last"}) {
		t.Fatalf("rows after wake = %v, want [first last]", got)
	}
	for i, done := range outcomes {
		err := <-done
		if (i == 1) != (err != nil) {
			t.Errorf("job %d outcome = %v", i, err)
		}
	}
}

func TestCommitWakeCoalescesAndDoesNotHoldUpWriter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchMax = 1
	s := h.open(t, opts)
	for i := range 20 {
		// The consumer intentionally does not read while twenty commits finish.
		if err := s.Write(context.Background(), func(*sql.Tx) error { return nil }); err != nil {
			t.Fatalf("no-op Write %d: %v", i, err)
		}
	}
	waitForWake(t, s.CommitWake())
	select {
	case <-s.CommitWake():
		t.Fatal("more than one pending wake after twenty commits")
	default:
	}
	if err := s.Write(context.Background(), insert("next")); err != nil {
		t.Fatalf("Write after draining wake: %v", err)
	}
	waitForWake(t, s.CommitWake())
}

func TestCommitWakeMayFollowBatchWithEveryJobRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchMax = 1
	s := h.open(t, opts)
	refused := errors.New("refused")
	done, err := s.Submit(context.Background(), func(tx *sql.Tx) error {
		if err := insert("discarded")(tx); err != nil {
			return err
		}
		return refused
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForWake(t, s.CommitWake())
	if got := rawValues(t, h.path); len(got) != 0 {
		t.Fatalf("rows after wake = %v, want none", got)
	}
	if err := <-done; !errors.Is(err, refused) {
		t.Fatalf("job outcome = %v, want refused", err)
	}
}

func TestCommitWakeSurvivesCallerCancelAfterEnqueue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchMax = 1
	s := h.open(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	releaseJob := sync.OnceFunc(func() { close(release) })
	// Unblock the writer before store cleanup even when an assertion fails.
	t.Cleanup(releaseJob)
	returned := make(chan error, 1)
	go func() {
		returned <- s.Write(ctx, func(tx *sql.Tx) error {
			close(started)
			<-release
			return insert("after cancel")(tx)
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("queued job did not start")
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Write after caller cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write did not return on caller cancel")
	}
	releaseJob()
	waitForWake(t, s.CommitWake())
	if got := rawValues(t, h.path); !slices.Equal(got, []string{"after cancel"}) {
		t.Fatalf("rows after canceled caller = %v, want [after cancel]", got)
	}
}

func TestCommitWakeClosesAfterFinalFlush(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s, err := store.Open(context.Background(), h.options(itemsV1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	done, err := s.Submit(context.Background(), insert("final"))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked with no wake consumer")
	}
	if err := <-done; err != nil {
		t.Fatalf("final job: %v", err)
	}
	waitForWake(t, s.CommitWake())
	select {
	case _, ok := <-s.CommitWake():
		if ok {
			t.Fatal("wake channel held an extra wake after Close")
		}
	default:
		t.Fatal("wake channel remained open after Close")
	}
	if got := rawValues(t, h.path); !slices.Equal(got, []string{"final"}) {
		t.Fatalf("rows after Close = %v, want [final]", got)
	}
}

func waitForWake(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case _, ok := <-wake:
		if !ok {
			t.Fatal("wake channel closed before commit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for committed batch")
	}
}
