package statedb_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Token inserts, first enrolls and renewals racing on one store each write
// their one row, and every committed change has its own hub_seq, rising in
// commit (id) order up to the counter.
func TestConcurrentActionsGetDistinctRisingHubSeqs(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	ctx := context.Background()
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	var signs atomic.Int32
	for i := range byte(n) {
		wg.Go(func() {
			tok := sampleToken("local:alice", fmt.Sprintf("race-%d", i), 10+i)
			errs <- statedb.InsertEnrollToken(ctx, s, tok)
		})
		wg.Go(func() {
			_, err := statedb.Renew(ctx, s, renewal(20+i, fmt.Sprintf("e%d", i), &signs))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Enroll each new token once, concurrently.
	for i := range byte(n) {
		wg.Go(func() {
			key := statedb.EnrollKey{
				SecretHash: sha256.Sum256([]byte{10 + i, 1, 2, 3}), InstallID: "i",
				CSRKeyHash: sha256.Sum256([]byte{40 + i}),
			}
			id := fmt.Sprintf("01jmachine00000000000000%02d", 10+i)
			if _, err := statedb.Enroll(ctx, s, newMachine(key, id, beforeTTL)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, dir)
	// 1 token + 1 enroll from enrolledStore, then n tokens, n renewals, n enrolls.
	if want := 2 + 3*n; len(rows) != want {
		t.Fatalf("%d audit rows, want %d", len(rows), want)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].HubSeq <= rows[i-1].HubSeq {
			t.Errorf("row %d hub_seq %d is not above row %d's %d", rows[i].ID, rows[i].HubSeq, rows[i-1].ID, rows[i-1].HubSeq)
		}
	}
	// Each enroll and renewal also stamped a machine: 2 + 2n + n numbers in all.
	if got, want := hubSeq(t, dir), int64(len(rows)+1+2*n); got != want {
		t.Errorf("hub_seq counter %d, want %d", got, want)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT count(DISTINCT hub_seq) FROM machines`); got != 1+n {
		t.Errorf("%d distinct machine hub_seqs, want %d", got, 1+n)
	}
}
