package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

func TestWriterSplitsABacklogIntoBatchesOfAtMost100(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	ctx := context.Background()

	// Hold the writer inside the first job so 250 more queue up behind it.
	started, release := make(chan struct{}), make(chan struct{})
	first, err := s.Submit(ctx, func(tx *sql.Tx) error {
		close(started)
		<-release
		return insert("0")(tx)
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started
	var pending []<-chan error
	for i := 1; i <= 250; i++ {
		done, err := s.Submit(ctx, insert(strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		pending = append(pending, done)
	}
	close(release)
	for i, done := range append([]<-chan error{first}, pending...) {
		if err := <-done; err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}

	// Batches of 1 (the held job), 100, 100 and 50.
	body := h.scrape(t)
	db := `db="state"`
	checks := []struct {
		series string
		labels []string
		want   float64
	}{
		{"fleet_hub_writer_batch_messages_count", nil, 4},
		{"fleet_hub_writer_batch_messages_sum", nil, 251},
		{"fleet_hub_writer_batch_messages_bucket", []string{`le="1"`}, 1},
		{"fleet_hub_writer_batch_messages_bucket", []string{`le="50"`}, 2},
		{"fleet_hub_writer_batch_messages_bucket", []string{`le="75"`}, 2},
		{"fleet_hub_writer_batch_messages_bucket", []string{`le="100"`}, 4},
		{"fleet_hub_writer_batch_seconds_count", nil, 4},
		{"fleet_hub_writer_queue_age_seconds_count", nil, 251},
		{"fleet_hub_writer_jobs_total", []string{`result="committed"`}, 251},
	}
	for _, c := range checks {
		if got := sample(body, c.series, append(c.labels, db)...); got != c.want {
			t.Errorf("%s%v = %v, want %v", c.series, c.labels, got, c.want)
		}
	}

	want := make([]string, 251)
	for i := range want {
		want[i] = strconv.Itoa(i)
	}
	if got := rawValues(t, h.path); !slices.Equal(got, want) {
		t.Errorf("rows are not in submission order: got %v", got)
	}
}

func TestWriterBatchWindowIs250ms(t *testing.T) {
	t.Parallel()
	const window = 250 * time.Millisecond
	cases := []struct {
		name        string
		run         func(t *testing.T, s *store.Store)
		wantBatches float64
	}{
		{"a lone job waits the window, then commits", func(t *testing.T, s *store.Store) {
			t.Helper()
			start := time.Now()
			if err := s.Write(context.Background(), insert("a")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if el := time.Since(start); el < window || el > window+2*time.Second {
				t.Errorf("lone job committed after %v, want about %v", el, window)
			}
		}, 1},
		{"jobs spread inside the window share one batch", func(t *testing.T, s *store.Store) {
			t.Helper()
			var pending []<-chan error
			for i := range 3 {
				done, err := s.Submit(context.Background(), insert(strconv.Itoa(i)))
				if err != nil {
					t.Fatalf("Submit: %v", err)
				}
				pending = append(pending, done)
				time.Sleep(25 * time.Millisecond)
			}
			for _, done := range pending {
				if err := <-done; err != nil {
					t.Fatalf("job: %v", err)
				}
			}
		}, 1},
		{"a job after a commit starts a new batch", func(t *testing.T, s *store.Store) {
			t.Helper()
			for _, v := range []string{"a", "b"} {
				if err := s.Write(context.Background(), insert(v)); err != nil {
					t.Fatalf("Write %s: %v", v, err)
				}
			}
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			s := h.open(t, h.options(itemsV1))
			tc.run(t, s)
			if got := sample(h.scrape(t), "fleet_hub_writer_batch_messages_count", `db="state"`); got != tc.wantBatches {
				t.Errorf("batches = %v, want %v", got, tc.wantBatches)
			}
		})
	}
}

func TestWriteReturnsOnlyAfterTheCommitIsVisible(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	for i := range 3 {
		v := strconv.Itoa(i)
		if err := s.Write(context.Background(), insert(v)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		// A separate connection sees the row the moment Write returns.
		if got := rawValues(t, h.path); len(got) != i+1 || got[i] != v {
			t.Fatalf("after Write(%s) another connection sees %v", v, got)
		}
	}
}

func TestAFailedJobRollsBackAloneAndTheBatchCommits(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	ctx := context.Background()
	jobs := []store.ApplyFunc{
		insert("a"),
		func(tx *sql.Tx) error { // writes "c", then breaks the unique constraint
			if err := insert("c")(tx); err != nil {
				return err
			}
			return insert("a")(tx)
		},
		insert("b"),
	}
	var pending []<-chan error
	for _, j := range jobs {
		done, err := s.Submit(ctx, j)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		pending = append(pending, done)
	}
	results := make([]error, len(pending))
	for i, done := range pending {
		results[i] = <-done
	}
	if results[0] != nil || results[1] == nil || results[2] != nil {
		t.Fatalf("results = %v, want [nil, constraint error, nil]", results)
	}
	if got := rawValues(t, h.path); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("rows = %v, want [a b]: the failed job's own write must be undone", got)
	}
	body := h.scrape(t)
	for result, want := range map[string]float64{"committed": 2, "failed": 1} {
		if got := sample(body, "fleet_hub_writer_jobs_total", `db="state"`, `result="`+result+`"`); got != want {
			t.Errorf("jobs_total{result=%s} = %v, want %v", result, got, want)
		}
	}
	if got := sample(body, "fleet_hub_writer_batch_messages_count", `db="state"`); got != 1 {
		t.Errorf("batches = %v, want 1", got)
	}
}

func TestABrokenTransactionFailsEveryJobInTheBatchAndTheWriterRecovers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	ctx := context.Background()
	jobs := []store.ApplyFunc{
		insert("x"),
		func(tx *sql.Tx) error { // misuses the transaction: ends it under the writer
			_, err := tx.ExecContext(ctx, "ROLLBACK")
			return err
		},
		insert("y"),
	}
	var pending []<-chan error
	for _, j := range jobs {
		done, err := s.Submit(ctx, j)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		pending = append(pending, done)
	}
	for i, done := range pending {
		if err := <-done; err == nil {
			t.Errorf("job %d: nil error, want the batch failure", i)
		}
	}
	if got := rawValues(t, h.path); len(got) != 0 {
		t.Errorf("rows = %v, want none from the failed batch", got)
	}
	if got := sample(h.scrape(t), "fleet_hub_writer_jobs_total", `db="state"`, `result="aborted"`); got != 3 {
		t.Errorf("jobs_total{result=aborted} = %v, want 3", got)
	}
	failed := h.logs.events(t, "store.batch_failed")
	if len(failed) != 1 || failed[0]["level"] != "error" || failed[0]["component"] != "store" ||
		failed[0]["db"] != "state" || failed[0]["jobs"] != float64(3) || failed[0]["err_kind"] != "io" ||
		failed[0]["sqlite_code"] != float64(1) { // RELEASE of a savepoint the job rolled away: SQLITE_ERROR
		t.Errorf("store.batch_failed log lines = %v, want one error line for 3 jobs with err_kind io and sqlite_code 1", failed)
	}
	if err := s.Write(ctx, insert("z")); err != nil {
		t.Fatalf("Write after a failed batch: %v", err)
	}
	if got := rawValues(t, h.path); !slices.Equal(got, []string{"z"}) {
		t.Errorf("rows = %v, want [z]", got)
	}
}

func TestCloseCommitsQueuedJobsThenRefusesNewWork(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s, err := store.Open(context.Background(), h.options(itemsV1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var pending []<-chan error
	for i := range 10 {
		done, err := s.Submit(context.Background(), insert(fmt.Sprint(i)))
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		pending = append(pending, done)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, done := range pending {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("queued job %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("queued job %d was never answered: Close dropped it", i)
		}
	}
	if got := rawValues(t, h.path); len(got) != 10 {
		t.Errorf("after Close, %d rows, want 10", len(got))
	}
	if _, err := s.Submit(context.Background(), insert("late")); !errors.Is(err, store.ErrClosed) {
		t.Errorf("Submit after Close: %v, want ErrClosed", err)
	}
	if err := s.Read(context.Background(), func(*sql.Tx) error { return nil }); !errors.Is(err, store.ErrClosed) {
		t.Errorf("Read after Close: %v, want ErrClosed", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestSubmitWaitsWhileTheQueueIsFullAndHonoursItsContext(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.QueueSize = 1
	s := h.open(t, opts)
	started, release := make(chan struct{}), make(chan struct{})
	held, err := s.Submit(context.Background(), func(*sql.Tx) error { close(started); <-release; return nil })
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started
	queued, err := s.Submit(context.Background(), insert("queued"))
	if err != nil {
		t.Fatalf("Submit into the one free slot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Submit(ctx, insert("blocked")); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Submit into a full queue: %v, want context.DeadlineExceeded", err)
	}
	close(release)
	for _, done := range []<-chan error{held, queued} {
		if err := <-done; err != nil {
			t.Fatalf("job: %v", err)
		}
	}
}

// The writer reports how long its oldest unanswered job has waited, so the
// WRITER_BATCH_LAG MAIN measure of 05b has a reading (issue #41): 0 with
// nothing waiting, growing while a batch is held, back to 0 once the
// batch commits.
func TestWriterReportsOldestJobAge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	if got := s.OldestJobAge(time.Now()); got != 0 {
		t.Fatalf("OldestJobAge with nothing waiting = %v, want 0", got)
	}

	started, release := make(chan struct{}), make(chan struct{})
	held, err := s.Submit(context.Background(), func(*sql.Tx) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := s.OldestJobAge(time.Now()); got > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("OldestJobAge stayed 0 while a batch was held")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatalf("job: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for s.OldestJobAge(time.Now()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("OldestJobAge stayed set after the batch committed")
		}
		time.Sleep(time.Millisecond)
	}
}
