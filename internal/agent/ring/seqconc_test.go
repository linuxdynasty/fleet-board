package ring_test

import (
	"context"
	"sync"
	"testing"
)

// One Seqs is shared by every goroutine that emits events (and the
// heartbeat reads Last, and a new stream sets Floor): under -race, seqs
// handed out together are all different, each goroutine sees its own rise,
// and Last is never below a seq already handed out.
func TestSeqsAreSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	seqs, err := db.Seqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const workers, per = 8, 400
	got := make([][]uint64, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			var prev uint64
			for i := range per {
				v, err := seqs.Next(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if v <= prev {
					t.Errorf("worker %d: seq %d after %d", w, v, prev)
				}
				if last := seqs.Last(); last < v {
					t.Errorf("worker %d: Last %d is below the seq %d already handed out", w, last, v)
				}
				if i%100 == 0 {
					if err := seqs.Floor(ctx, v+500); err != nil {
						t.Error(err)
					}
				}
				prev = v
				got[w] = append(got[w], v)
			}
		})
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for _, list := range got {
		for _, v := range list {
			if seen[v] {
				t.Errorf("seq %d handed out twice", v)
			}
			seen[v] = true
		}
	}
	if len(seen) != workers*per {
		t.Errorf("%d different seqs, want %d", len(seen), workers*per)
	}
}
