package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// BenchmarkWriterBatch is the cost of one durable batch through the single
// writer: each op submits size jobs and waits for every outcome. BatchMax
// equals size, so each batch commits as soon as it fills and the 250 ms window
// never runs; one op is one BEGIN IMMEDIATE ... COMMIT with the ADR 0014
// pragmas (synchronous=FULL, fullfsync), so ns/op is mostly the commit's
// fsync. Telemetry is off: this measures the writer, not the exporters.
// CI compares base and head with benchstat (scripts/bench-report.sh).
func BenchmarkWriterBatch(b *testing.B) {
	for _, size := range []int{1, 10, store.MaxBatch} {
		b.Run(fmt.Sprintf("jobs=%d", size), func(b *testing.B) {
			s, err := store.Open(context.Background(), store.Options{
				Path:        filepath.Join(b.TempDir(), "state.db"),
				Name:        "state",
				Migrations:  []store.Migration{itemsV1},
				BatchMax:    size,
				BatchWindow: store.DefaultBatchWindow,
			})
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			b.Cleanup(func() { _ = s.Close() })
			ctx := context.Background()
			done := make([]<-chan error, size)
			n := 0
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for i := range done {
					n++
					if done[i], err = s.Submit(ctx, insertN(n)); err != nil {
						b.Fatalf("Submit: %v", err)
					}
				}
				for i, ch := range done {
					if err := <-ch; err != nil {
						b.Fatalf("job %d: %v", i, err)
					}
				}
			}
			b.ReportMetric(float64(size), "jobs/batch")
		})
	}
}

// insertN inserts a unique row, so no job hits the UNIQUE constraint.
func insertN(n int) store.ApplyFunc {
	v := strconv.Itoa(n)
	return func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO items(v) VALUES (?)", v)
		return err
	}
}
