package main

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A50 steady rates for state.db (04's load totals minus the log index, which moves
// to logs.db): pane summaries are overwritten in place (ADR 0013).
const (
	panes                = 500
	machines             = 50
	summaryPerSecDefault = 100 // 2 KB each; see -summary-rate
	eventsPerSec         = 7   // never-dropped kinds, 0.5 KB
	miscPerSec           = 20  // sync upserts, last_seen, resource, usage, commands
	burstPerSec          = 2500
	batchMax             = 100
	batchWindowDefault   = 50 * time.Millisecond // see -batch-window
	browsers             = 10
	visiblePerView       = 30
)

type msg struct {
	kind int // 0 summary, 1 event, 2 misc
	at   time.Time
}

const stateSchema = `
CREATE TABLE pane_summaries(pane_key TEXT PRIMARY KEY, machine_id INTEGER, body BLOB, updated_at INTEGER);
CREATE TABLE events(machine_id INTEGER, seq INTEGER, kind INTEGER, at INTEGER, body BLOB, PRIMARY KEY(machine_id, seq));
CREATE INDEX events_at ON events(at);
CREATE TABLE misc(id INTEGER PRIMARY KEY, k TEXT, at INTEGER, body BLOB);
`

func runState(path string, burst bool) {
	w, r := open(path)
	for _, s := range strings.Split(strings.TrimSpace(stateSchema), ";\n") {
		mustExec(w, s)
	}
	for i := 0; i < panes; i++ {
		mustExec(w, "INSERT INTO pane_summaries VALUES (?,?,?,?)", paneKey(i), i%machines, randBytes(2048), 0)
	}

	in := make(chan msg, 20_000)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	produce := func(kind, perSec int) {
		defer wg.Done()
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		acc := 0.0
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				acc += float64(perSec) / 100
				for ; acc >= 1; acc-- {
					in <- msg{kind, now}
				}
			}
		}
	}
	rates := [][2]int{{0, *summaryRate}, {1, eventsPerSec}, {2, miscPerSec}}
	if burst {
		rates = append(rates, [2]int{1, burstPerSec})
	}
	for _, rt := range rates {
		wg.Add(1)
		go produce(rt[0], rt[1])
	}

	var readLat [3][]time.Duration
	var rmu sync.Mutex
	for b := 0; b < browsers; b++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
				}
				d := readers(r)
				rmu.Lock()
				for i := range d {
					readLat[i] = append(readLat[i], d[i])
				}
				rmu.Unlock()
			}
		}()
	}

	var commitLat, msgLag []time.Duration
	var rows atomic.Int64
	seq := make([]int64, machines)
	var walMax int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		var batch []msg
		flush := func() {
			if len(batch) == 0 {
				return
			}
			t := time.Now()
			tx := must(w.Begin())
			up := must(tx.Prepare("UPDATE pane_summaries SET body=?, updated_at=? WHERE pane_key=?"))
			ev := must(tx.Prepare("INSERT INTO events VALUES (?,?,?,?,?)"))
			mi := must(tx.Prepare("INSERT INTO misc(k, at, body) VALUES (?,?,?)"))
			for _, m := range batch {
				now := time.Now().UnixMilli()
				switch m.kind {
				case 0:
					must(up.Exec(blob(*summaryBytes), now, paneKey(rand.IntN(panes))))
				case 1:
					mc := rand.IntN(machines)
					seq[mc]++
					must(ev.Exec(mc, seq[mc], 1, now, blob(512)))
				case 2:
					must(mi.Exec("k", now, blob(512)))
				}
			}
			check(tx.Commit())
			end := time.Now()
			commitLat = append(commitLat, end.Sub(t))
			for _, m := range batch {
				msgLag = append(msgLag, end.Sub(m.at))
			}
			rows.Add(int64(len(batch)))
			batch = batch[:0]
			if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > walMax {
				walMax = fi.Size()
			}
		}
		deadline := time.NewTimer(*batchWindow)
		for {
			select {
			case m, ok := <-in:
				if !ok {
					flush()
					return
				}
				batch = append(batch, m)
				if len(batch) >= batchMax {
					flush()
					deadline.Reset(*batchWindow)
				}
			case <-deadline.C:
				flush()
				deadline.Reset(*batchWindow)
			}
		}
	}()

	w0 := diskWritten()
	start := time.Now()
	time.Sleep(*dur)
	close(stop)
	wg.Wait()
	close(in)
	<-done
	el := time.Since(start)
	wb := diskWritten() - w0

	fmt.Printf("rows=%d (%.0f/s) commits=%d (%.1f/s) disk_written=%.1f MB (%.0f KB/s) wal_max=%.1f MB\n",
		rows.Load(), float64(rows.Load())/el.Seconds(), len(commitLat), float64(len(commitLat))/el.Seconds(),
		float64(wb)/1e6, float64(wb)/1e3/el.Seconds(), float64(walMax)/1e6)
	report("writer_commit", commitLat)
	report("msg_enqueue_to_commit", msgLag)
	report("read_sidebar_500", readLat[0])
	report("read_visible_30", readLat[1])
	report("read_timeline_50", readLat[2])
}

// readers runs one browser refresh: sidebar, visible summaries, a timeline.
func readers(r *sql.DB) [3]time.Duration {
	var d [3]time.Duration
	t := time.Now()
	scanAll(must(r.Query("SELECT pane_key, machine_id, updated_at FROM pane_summaries")))
	d[0] = time.Since(t)

	keys := make([]any, visiblePerView)
	for i := range keys {
		keys[i] = paneKey(rand.IntN(panes))
	}
	t = time.Now()
	scanAll(must(r.Query("SELECT body FROM pane_summaries WHERE pane_key IN (?"+strings.Repeat(",?", visiblePerView-1)+")", keys...)))
	d[1] = time.Since(t)

	t = time.Now()
	scanAll(must(r.Query("SELECT seq, kind, at, body FROM events WHERE machine_id=? ORDER BY seq DESC LIMIT 50", rand.IntN(machines))))
	d[2] = time.Since(t)
	return d
}

func scanAll(rows *sql.Rows) {
	defer rows.Close()
	cols := must(rows.Columns())
	vals := make([]any, len(cols))
	for i := range vals {
		vals[i] = new(any)
	}
	for rows.Next() {
		check(rows.Scan(vals...))
	}
	check(rows.Err())
}

func paneKey(i int) string { return fmt.Sprintf("m%02d/default/%d/%%%d", i%machines, 1, i) }
