package conn

import (
	"testing"
	"time"
)

// Ruling round 3, 3c: a take never releases more than the tokens held. The
// first drain is capped at the burst (64) whatever the limit asked, and
// after it only what the rate refilled may go (10 ms at 200/s is 2).
func TestATakeNeverReleasesMoreThanTheTokensHeld(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_000, 0)
	b := newEventBucket()
	b.last, b.now = at, func() time.Time { return at }
	if n, _ := b.take(1000); n != eventBatch {
		t.Fatalf("first take %d, want the burst %d", n, eventBatch)
	}
	b.spend(eventBatch)
	if n, wait := b.take(eventBatch); n != 0 || wait != 5*time.Millisecond {
		t.Fatalf("an empty bucket gave %d and a wait of %v, want 0 and 5ms", n, wait)
	}
	at = at.Add(10 * time.Millisecond)
	if n, _ := b.take(eventBatch); n != 2 {
		t.Errorf("after 10 ms the take is %d, want 2 (200/s)", n)
	}
	at = at.Add(time.Hour)
	if n, _ := b.take(1000); n != eventBatch {
		t.Errorf("after an idle hour the take is %d, want the burst cap %d", n, eventBatch)
	}
}
