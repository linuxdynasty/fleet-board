//go:build darwin

package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// TestDurableCommitsUseFullFsyncOnDarwin repeats spike S1's check (04, Q1):
// on macOS a commit that reaches the drive with F_FULLFSYNC costs about 4 ms,
// one that stops at fsync() about 50 µs. The store's commits must cost the
// first; the same driver with fullfsync off is the baseline.
func TestDurableCommitsUseFullFsyncOnDarwin(t *testing.T) {
	if testing.Short() {
		t.Skip("times real disk flushes")
	}
	const n = 60
	h := newHarness(t)
	opts := h.options(itemsV1)
	opts.BatchWindow = time.Microsecond // one job per batch: each Write is one commit
	s := h.open(t, opts)
	storeLat := make([]time.Duration, 0, n)
	for i := range n {
		start := time.Now()
		if err := s.Write(context.Background(), insert(strconv.Itoa(i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
		storeLat = append(storeLat, time.Since(start))
	}

	off := rawCommitLatency(t, filepath.Join(t.TempDir(), "off.db"), 0, n)
	on := rawCommitLatency(t, filepath.Join(t.TempDir(), "on.db"), 1, n)
	storeP50 := p50(storeLat)
	t.Logf("commit p50: store %v; same driver with fullfsync=1 %v, with fullfsync=0 %v", storeP50, p50(on), p50(off))
	if storeP50 < time.Millisecond {
		t.Errorf("store commit p50 %v is under 1 ms: F_FULLFSYNC is not reaching the drive", storeP50)
	}
	if storeP50 < 5*p50(off) {
		t.Errorf("store commit p50 %v is not well above the fullfsync=0 baseline %v", storeP50, p50(off))
	}
}

func rawCommitLatency(t *testing.T, path string, fullfsync, n int) []time.Duration {
	t.Helper()
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=fullfsync(" + strconv.Itoa(fullfsync) + ")"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("raw create: %v", err)
	}
	lat := make([]time.Duration, 0, n)
	for i := range n {
		start := time.Now()
		if _, err := db.Exec("INSERT INTO t(v) VALUES (?)", strconv.Itoa(i)); err != nil {
			t.Fatalf("raw insert: %v", err)
		}
		lat = append(lat, time.Since(start))
	}
	return lat
}

func p50(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2]
}
