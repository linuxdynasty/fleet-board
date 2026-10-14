package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// The writer records when its queue became continuously full, so the
// WRITER_QUEUE_FULL measure of 05b has a reading (issue #41, slice 1). A
// full queue nobody waited on is not a full period: the stamp is set by a
// Submit that finds no room, and cleared once the writer sees room again.
func TestWriterRecordsHowLongItsQueueHasBeenFull(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.QueueSize = 2
	s := h.open(t, opts)
	ctx := context.Background()

	if got := s.QueueFullSince(); !got.IsZero() {
		t.Fatalf("QueueFullSince with room in the queue = %v, want zero", got)
	}

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
	if got := s.QueueFullSince(); !got.IsZero() {
		t.Fatalf("QueueFullSince on a full queue nobody waited on = %v, want zero", got)
	}

	type waited struct {
		done <-chan error
		err  error
	}
	arrived := make(chan waited, 1)
	go func() {
		done, err := s.Submit(ctx, insert("waits"))
		arrived <- waited{done, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for s.QueueFullSince().IsZero() {
		if time.Now().After(deadline) {
			t.Fatalf("QueueFullSince stayed zero while a Submit waited on a full queue")
		}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(300 * time.Millisecond)
	db := `db="state"`
	first := sample(h.scrape(t), "fleet_hub_writer_queue_full_seconds", db)
	if first < 0.25 {
		t.Errorf("writer_queue_full_seconds = %v, want at least 0.25 while the queue stays full", first)
	}
	time.Sleep(200 * time.Millisecond)
	if later := sample(h.scrape(t), "fleet_hub_writer_queue_full_seconds", db); later < first+0.15 {
		t.Errorf("writer_queue_full_seconds went from %v to %v in 200 ms; it must keep growing", first, later)
	}

	close(release)
	w := <-arrived
	if w.err != nil {
		t.Fatalf("blocked Submit: %v", w.err)
	}
	for _, done := range append(append([]<-chan error{held}, queued...), w.done) {
		if err := <-done; err != nil {
			t.Fatalf("job: %v", err)
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	for !s.QueueFullSince().IsZero() {
		if time.Now().After(deadline) {
			t.Fatalf("QueueFullSince stayed %v after the queue drained, want zero", s.QueueFullSince())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := sample(h.scrape(t), "fleet_hub_writer_queue_full_seconds", db); got != 0 {
		t.Errorf("writer_queue_full_seconds with room in the queue = %v, want 0", got)
	}
}
