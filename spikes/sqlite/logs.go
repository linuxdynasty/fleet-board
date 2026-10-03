package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"
)

// A50 log shipping (04's load model): 300 capturing panes, one chunk per pane every
// 10 s = 30 chunks/s, committed once per second with the index in the same row.
const (
	chunksPerSec    = 30
	chunksPerSess   = 360 // one hour of 10 s chunks
	logsBatchWindow = time.Second
)

const logsSchema = `
CREATE TABLE log_sessions(session_id INTEGER PRIMARY KEY, machine_id INTEGER, first_at INTEGER, last_at INTEGER, bytes INTEGER, final INTEGER);
CREATE TABLE log_chunks(id INTEGER PRIMARY KEY, session_id INTEGER, stream INTEGER, raw_offset INTEGER, raw_length INTEGER, first_at INTEGER, last_at INTEGER, gap_before INTEGER, data BLOB);
CREATE UNIQUE INDEX log_chunks_off ON log_chunks(session_id, stream, raw_offset);
CREATE INDEX log_chunks_time ON log_chunks(session_id, stream, first_at)
`

func runLogs(path string) {
	w, r := open(path)
	// auto_vacuum must be chosen before the first table exists.
	mustExec(w, "PRAGMA auto_vacuum=INCREMENTAL")
	mustExec(w, "VACUUM")
	for _, s := range strings.Split(strings.TrimSpace(logsSchema), ";\n") {
		mustExec(w, s)
	}

	// Prefill: whole sessions of chunkSize bytes, oldest first.
	t0 := time.Now()
	sess := 0
	ins := "INSERT INTO log_chunks(session_id, stream, raw_offset, raw_length, first_at, last_at, gap_before, data) VALUES (?,?,?,?,?,?,?,?)"
	for n := 0; n < *prefill; {
		tx := must(w.Begin())
		st := must(tx.Prepare(ins))
		for i := 0; i < 20 && n < *prefill; i++ {
			sess++
			must(tx.Exec("INSERT INTO log_sessions VALUES (?,?,?,?,?,1)", sess, sess%machines, sess, sess, chunksPerSess**chunkSize))
			for c := 0; c < chunksPerSess && n < *prefill; c, n = c+1, n+1 {
				must(st.Exec(sess, 0, c*75_000, 75_000, sess*3600+c*10, sess*3600+c*10+10, 0, blob(*chunkSize)))
			}
		}
		check(tx.Commit())
	}
	fmt.Printf("prefill: %d chunks, %d sessions, %.0f MB, %v\n", *prefill, sess, fileMB(path), time.Since(t0).Round(time.Millisecond))

	// Steady ingest with concurrent replay reads.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var replay, point []time.Duration
	var rmu sync.Mutex
	for b := 0; b < browsers; b++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
				}
				s := 1 + rand.IntN(sess)
				a := time.Now()
				scanAll(must(r.Query("SELECT raw_offset, data FROM log_chunks WHERE session_id=? AND stream=0 ORDER BY raw_offset", s)))
				b := time.Now()
				scanAll(must(r.Query("SELECT data FROM log_chunks WHERE session_id=? AND stream=0 AND raw_offset<=? ORDER BY raw_offset DESC LIMIT 1", s, rand.IntN(chunksPerSess)*75_000)))
				c := time.Now()
				rmu.Lock()
				replay, point = append(replay, b.Sub(a)), append(point, c.Sub(b))
				rmu.Unlock()
			}
		}()
	}
	live := sess + 1
	off := make([]int, 300)
	var commitLat []time.Duration
	w0 := diskWritten()
	start := time.Now()
	tick := time.NewTicker(logsBatchWindow)
	for time.Since(start) < *dur {
		<-tick.C
		a := time.Now()
		tx := must(w.Begin())
		st := must(tx.Prepare(ins))
		for i := 0; i < chunksPerSec; i++ {
			p := rand.IntN(len(off))
			must(st.Exec(live+p, 0, off[p], 75_000, a.Unix(), a.Unix()+10, 0, blob(*chunkSize)))
			off[p] += 75_000
		}
		check(tx.Commit())
		commitLat = append(commitLat, time.Since(a))
	}
	tick.Stop()
	close(stop)
	wg.Wait()
	wb := diskWritten() - w0
	el := time.Since(start)
	fmt.Printf("ingest: %d chunks/s x %d B, disk_written=%.0f KB/s\n", chunksPerSec, *chunkSize, float64(wb)/1e3/el.Seconds())
	report("logs_commit_1s_batch", commitLat)
	report("replay_session_1h", replay)
	report("point_lookup_offset", point)

	// Per-session delete (one audited action), then size-cap pruning of the
	// oldest 10 %, then returning the space to the filesystem.
	a := time.Now()
	tx := must(w.Begin())
	must(tx.Exec("DELETE FROM log_chunks WHERE session_id=?", sess/2))
	must(tx.Exec("DELETE FROM log_sessions WHERE session_id=?", sess/2))
	check(tx.Commit())
	fmt.Printf("delete_one_session: %v\n", time.Since(a).Round(time.Microsecond))

	before := fileMB(path)
	a = time.Now()
	var perSess []time.Duration
	for s := 1; s <= sess/10; s++ {
		b := time.Now()
		tx := must(w.Begin())
		must(tx.Exec("DELETE FROM log_chunks WHERE session_id=?", s))
		must(tx.Exec("DELETE FROM log_sessions WHERE session_id=?", s))
		check(tx.Commit())
		perSess = append(perSess, time.Since(b))
	}
	fmt.Printf("prune_oldest_10pct: %d sessions in %v\n", sess/10, time.Since(a).Round(time.Millisecond))
	report("prune_tx_per_session", perSess)

	var free int
	check(w.QueryRow("PRAGMA freelist_count").Scan(&free))
	a = time.Now()
	var steps []time.Duration
	for {
		var f int
		check(w.QueryRow("PRAGMA freelist_count").Scan(&f))
		if f == 0 {
			break
		}
		b := time.Now()
		mustExec(w, "PRAGMA incremental_vacuum(2000)")
		steps = append(steps, time.Since(b))
	}
	mustExec(w, "PRAGMA wal_checkpoint(TRUNCATE)")
	fmt.Printf("incremental_vacuum: %d free pages in %v, file %.0f MB -> %.0f MB\n", free, time.Since(a).Round(time.Millisecond), before, fileMB(path))
	report("vacuum_step_2000pages", steps)
}

func fileMB(path string) float64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / 1e6
}
