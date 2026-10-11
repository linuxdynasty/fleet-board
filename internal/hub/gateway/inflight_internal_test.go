package gateway

import (
	"context"
	"errors"
	"testing"
)

// With no handler running, wait reports none even when its context has
// already ended (Shutdown's own drain used up the grace): a stop with
// nothing left to wait for is not a timeout. Both select cases are ready
// then, so each call is a fresh coin toss without the recheck.
func TestInflightWaitWithNoHandlerIsNoTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newInflight()
	f.add()
	f.done()
	for range 64 {
		if n, err := f.wait(ctx); n != 0 || err != nil {
			t.Fatalf("wait = %d, %v with no handler running, want 0, nil", n, err)
		}
	}
}

// A handler still running when the context ends is a timeout, counted.
func TestInflightWaitCountsHandlersAtTheEnd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newInflight()
	f.add()
	defer f.done()
	if n, err := f.wait(ctx); n != 1 || !errors.Is(err, context.Canceled) {
		t.Errorf("wait = %d, %v with one handler running, want 1, context.Canceled", n, err)
	}
}
