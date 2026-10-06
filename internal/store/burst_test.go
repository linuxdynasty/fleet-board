package store_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// The A50 restart burst of 04 (Q1) through the real writer: spike S1's burst
// mix, one store job per message, so the per-job savepoint and result channel
// are paid. Budget: the writer keeps up with about 2,500 rows/s and spends at
// most 70 % of its time in transactions. It is a wall-clock measurement, so
// it runs only when asked and never under -short:
//
//	FLEET_STORE_A50=1 CGO_ENABLED=0 go test -count=1 -run A50 -v ./internal/store/
const a50Env = "FLEET_STORE_A50"

var burstSchema = store.Migration{Version: 1, Name: "burst", SQL: `
CREATE TABLE pane_summaries(pane_key TEXT PRIMARY KEY, machine_id INTEGER, body BLOB, updated_time INTEGER);
CREATE TABLE events(machine_id INTEGER, seq INTEGER, kind INTEGER, observed_time INTEGER, body BLOB, PRIMARY KEY(machine_id, seq));
CREATE INDEX events_time ON events(observed_time);
CREATE TABLE misc(id INTEGER PRIMARY KEY, k TEXT, observed_time INTEGER, body BLOB);`}

const (
	burstPanes    = 500
	burstMachines = 50
)

type burstRate struct {
	kind  string
	perS  float64
	bytes int
}

// S1's burst scenario: 2,500 replayed events/s on top of the A50 steady mix.
var burstRates = []burstRate{
	{"summary", 100, 2048},
	{"event", 7 + 2500, 512},
	{"misc", 20, 512},
}

func TestWriterMeetsTheA50RestartBurst(t *testing.T) {
	if testing.Short() || os.Getenv(a50Env) != "1" {
		t.Skipf("wall-clock measurement; set %s=1 and run without -short", a50Env)
	}
	dur := 10 * time.Second
	if v, err := time.ParseDuration(os.Getenv(a50Env + "_DURATION")); err == nil {
		dur = v
	}
	h := newHarness(t)
	opts := h.options(burstSchema)
	s := h.open(t, opts)
	ctx := context.Background()
	blobs := map[int][][]byte{}
	for _, r := range burstRates {
		for range 256 {
			b := make([]byte, r.bytes)
			_, _ = rand.Read(b)
			blobs[r.bytes] = append(blobs[r.bytes], b)
		}
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		for i := range burstPanes {
			if _, err := tx.ExecContext(ctx, "INSERT INTO pane_summaries VALUES (?, ?, ?, 0)", paneKey(i), i%burstMachines, blobs[2048][0]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("prefill: %v", err)
	}
	before := h.scrape(t)

	type pending struct {
		at   time.Time
		done <-chan error
	}
	queue := make(chan pending, 100_000)
	var waits []time.Duration
	var failed int
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for p := range queue { // results arrive in submission order
			if err := <-p.done; err != nil {
				failed++
			}
			waits = append(waits, time.Since(p.at))
		}
	}()

	seq := make([]int64, burstMachines)
	acc := make([]float64, len(burstRates))
	submitted := 0
	tick := time.NewTicker(10 * time.Millisecond)
	start := time.Now()
	for now := range tick.C {
		if now.Sub(start) >= dur {
			break
		}
		for i, r := range burstRates {
			for acc[i] += r.perS / 100; acc[i] >= 1; acc[i]-- {
				body := blobs[r.bytes][mrand.IntN(256)] //nolint:gosec // G404: picks load-test rows and blobs; nothing secret depends on it
				var apply store.ApplyFunc
				switch r.kind {
				case "summary":
					key, ts := paneKey(mrand.IntN(burstPanes)), now.UnixMilli() //nolint:gosec // G404: picks load-test rows and blobs; nothing secret depends on it
					apply = func(tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "UPDATE pane_summaries SET body = ?, updated_time = ? WHERE pane_key = ?", body, ts, key)
						return err
					}
				case "event":
					m := mrand.IntN(burstMachines) //nolint:gosec // G404: picks load-test rows and blobs; nothing secret depends on it
					seq[m]++
					sq, ts := seq[m], now.UnixMilli()
					apply = func(tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "INSERT INTO events VALUES (?, ?, 1, ?, ?)", m, sq, ts, body)
						return err
					}
				default:
					ts := now.UnixMilli()
					apply = func(tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "INSERT INTO misc(k, observed_time, body) VALUES ('k', ?, ?)", ts, body)
						return err
					}
				}
				at := time.Now()
				done, err := s.Submit(ctx, apply)
				if err != nil {
					t.Fatalf("Submit: %v", err)
				}
				queue <- pending{at: at, done: done}
				submitted++
			}
		}
	}
	tick.Stop()
	close(queue)
	<-collected
	elapsed := time.Since(start)
	after := h.scrape(t)

	db := `db="state"`
	delta := func(name string, labels ...string) float64 {
		return sample(after, name, append(labels, db)...) - sample(before, name, append(labels, db)...)
	}
	batches := delta("fleet_hub_writer_batch_seconds_count")
	busy := delta("fleet_hub_writer_batch_seconds_sum") / elapsed.Seconds()
	batchP99 := histogramQuantile(0.99, before, after, "fleet_hub_writer_batch_seconds", db)
	rowsPerS := float64(len(waits)-failed) / elapsed.Seconds()
	slices.Sort(waits)
	waitP50, waitP99 := waits[len(waits)/2], waits[int(0.99*float64(len(waits)-1))]

	t.Logf("A50 burst for %v: submitted %d, committed %.0f rows/s, failed %d", elapsed.Round(time.Millisecond), submitted, rowsPerS, failed)
	t.Logf("batches %.0f (%.1f/s, mean %.0f jobs), writer busy %.1f %%, batch seconds mean %.1f ms p99 about %.1f ms (bucket interpolation)",
		batches, batches/elapsed.Seconds(), float64(submitted)/batches, 100*busy,
		1000*delta("fleet_hub_writer_batch_seconds_sum")/batches, 1000*batchP99)
	t.Logf("enqueue to commit: p50 %v, p99 %v", waitP50.Round(time.Millisecond), waitP99.Round(time.Millisecond))

	if failed > 0 {
		t.Errorf("%d jobs failed", failed)
	}
	if want := 0.95 * 2627; rowsPerS < want {
		t.Errorf("committed %.0f rows/s, want at least %.0f: the writer did not keep up", rowsPerS, want)
	}
	if busy > 0.70 {
		t.Errorf("writer busy %.1f %%, budget 70 %% (04, Q1)", 100*busy)
	}
	if waitP99 > time.Second {
		t.Errorf("enqueue to commit p99 %v, over the 1 s WRITER_BATCH_LAG warning (05b)", waitP99)
	}
}

func paneKey(i int) string { return fmt.Sprintf("m%02d/default/1/%%%d", i%burstMachines, i) }

// histogramQuantile estimates a quantile from the bucket counts added between
// two scrapes, interpolating linearly inside the bucket as Prometheus does.
func histogramQuantile(q float64, before, after, name, label string) float64 {
	type bucket struct{ le, count float64 }
	var bs []bucket
	for _, line := range strings.Split(after, "\n") {
		if !strings.HasPrefix(line, name+"_bucket{") || !strings.Contains(line, label) {
			continue
		}
		i := strings.Index(line, `le="`)
		j := strings.Index(line[i+4:], `"`)
		le, err := strconv.ParseFloat(line[i+4:i+4+j], 64)
		if err != nil {
			le = math.Inf(1)
		}
		leLabel := `le="` + line[i+4:i+4+j] + `"`
		c := sample(after, name+"_bucket", label, leLabel) - math.Max(0, sample(before, name+"_bucket", label, leLabel))
		bs = append(bs, bucket{le, c})
	}
	if len(bs) == 0 {
		return math.NaN()
	}
	total := bs[len(bs)-1].count
	rank, lower, prev := q*total, 0.0, 0.0
	for _, b := range bs {
		if b.count >= rank {
			if math.IsInf(b.le, 1) {
				return lower
			}
			return lower + (b.le-lower)*(rank-prev)/math.Max(b.count-prev, 1)
		}
		lower, prev = b.le, b.count
	}
	return lower
}
