package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// A writer stuck inside a job commits nothing, so the post-commit histograms
// stay silent; the queue depth, the oldest job's age and the give-up counter
// must show it anyway (05b, WRITER_BATCH_LAG).
func TestABlockedWriterShowsItsBacklog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.QueueSize = 2
	s := h.open(t, opts)
	ctx := context.Background()

	started, release := make(chan struct{}), make(chan struct{})
	held, err := s.Submit(ctx, func(*sql.Tx) error { close(started); <-release; return nil })
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started
	var queued []<-chan error
	for _, v := range []string{"q1", "q2"} {
		done, err := s.Submit(ctx, insert(v))
		if err != nil {
			t.Fatalf("Submit %s: %v", v, err)
		}
		queued = append(queued, done)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := s.Submit(short, insert("gives up")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit into a full queue: %v, want context.DeadlineExceeded", err)
	}

	time.Sleep(300 * time.Millisecond)
	db := `db="state"`
	first := h.scrape(t)
	if got := sample(first, "fleet_hub_writer_queue_depth", db); got != 2 {
		t.Errorf("writer_queue_depth = %v, want 2", got)
	}
	if got := sample(first, "fleet_hub_writer_queue_full_total", db); got != 1 {
		t.Errorf("writer_queue_full_total = %v, want 1", got)
	}
	age1 := sample(first, "fleet_hub_writer_oldest_job_age_seconds", db)
	if age1 < 0.5 { // the 250 ms window, then at least 300 ms held
		t.Errorf("writer_oldest_job_age_seconds = %v, want at least 0.5 while the writer is stuck", age1)
	}
	time.Sleep(200 * time.Millisecond)
	if age2 := sample(h.scrape(t), "fleet_hub_writer_oldest_job_age_seconds", db); age2 < age1+0.15 {
		t.Errorf("oldest job age went from %v to %v in 200 ms; it must keep growing", age1, age2)
	}

	close(release)
	for _, done := range append([]<-chan error{held}, queued...) {
		if err := <-done; err != nil {
			t.Fatalf("job: %v", err)
		}
	}
	after := h.scrape(t)
	if got := sample(after, "fleet_hub_writer_queue_depth", db); got != 0 {
		t.Errorf("writer_queue_depth after the backlog drained = %v, want 0", got)
	}
	if got := sample(after, "fleet_hub_writer_oldest_job_age_seconds", db); got != 0 {
		t.Errorf("writer_oldest_job_age_seconds with nothing waiting = %v, want 0", got)
	}
}
