package store_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func countItems(tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRow("SELECT count(*) FROM items").Scan(&n)
	return n, err
}

func TestReadersDoNotWaitForTheWriterAndSeeOnlyCommittedRows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	ctx := context.Background()
	if err := s.Write(ctx, insert("before")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	held, err := s.Submit(ctx, func(tx *sql.Tx) error {
		if err := insert("during")(tx); err != nil {
			return err
		}
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started // the write transaction is open and holds an uncommitted row

	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var n int
	if err := s.Read(readCtx, func(tx *sql.Tx) (err error) { n, err = countItems(tx); return err }); err != nil {
		t.Fatalf("Read during a write: %v", err)
	}
	if n != 1 {
		t.Errorf("during the write a reader saw %d rows, want 1 (committed only)", n)
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatalf("held job: %v", err)
	}
	if err := s.Read(ctx, func(tx *sql.Tx) (err error) { n, err = countItems(tx); return err }); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 2 {
		t.Errorf("after the commit a reader saw %d rows, want 2", n)
	}
}

func TestTheReadPoolHoldsTenConnections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := h.open(t, h.options(itemsV1))
	ctx := context.Background()

	entered, release := make(chan struct{}, 10), make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			errs <- s.Read(ctx, func(*sql.Tx) error { entered <- struct{}{}; <-release; return nil })
		})
	}
	for range 10 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("ten readers could not run at the same time")
		}
	}
	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err := s.Read(short, func(*sql.Tx) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an eleventh reader: %v, want context.DeadlineExceeded while ten are busy", err)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("reader: %v", err)
		}
	}
	if got := sample(h.scrape(t), "fleet_hub_read_pool_wait_seconds_count", `db="state"`); got < 10 {
		t.Errorf("read_pool_wait_seconds_count = %v, want at least 10", got)
	}
}
