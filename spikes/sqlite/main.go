// Spike S1 for docs/specs/04-technology-review.md: does embedded SQLite with
// one writer meet 04's load model (A50) on a pure-Go driver, and is a commit
// durable (F_FULLFSYNC) on macOS? Throwaway code; not part of fleetd.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "modernc.org/sqlite"
)

var (
	driver       = flag.String("driver", "modernc", "modernc | ncruces")
	fullfsync    = flag.Bool("fullfsync", true, "PRAGMA fullfsync (macOS F_FULLFSYNC)")
	syncMode     = flag.String("sync", "FULL", "PRAGMA synchronous")
	scenario     = flag.String("scenario", "commit", "commit | steady | burst | logs")
	dir          = flag.String("dir", "", "database directory (default: temp dir)")
	dur          = flag.Duration("dur", 60*time.Second, "measured duration")
	chunkSize    = flag.Int("chunk", 2048, "logs: compressed chunk bytes")
	prefill      = flag.Int("prefill", 500_000, "logs: chunks inserted before measuring")
	batchWindow  = flag.Duration("batch-window", 50*time.Millisecond, "writer batch window (03f: 50 ms)")
	summaryRate  = flag.Int("summary-rate", 100, "pane summary writes/s (100 = every update; ~17 = 30 s write-behind of 500 panes)")
	summaryBytes = flag.Int("summary-bytes", 2048, "pane summary bytes as stored (2048 raw; ~600 zstd)")
)

func dsn(path string, readOnly bool) (name, d string) {
	p := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(" + *syncMode + ")",
		fmt.Sprintf("_pragma=fullfsync(%d)", b2i(*fullfsync)),
		"_pragma=journal_size_limit(67108864)",
	}
	if readOnly {
		p = append(p, "_pragma=query_only(1)")
	} else {
		p = append(p, "_txlock=immediate")
	}
	name = map[string]string{"modernc": "sqlite", "ncruces": "sqlite3"}[*driver]
	return name, "file:" + path + "?" + strings.Join(p, "&")
}

// open returns a one-connection writer and a reader pool, as in ADR 0002.
func open(path string) (w, r *sql.DB) {
	n, d := dsn(path, false)
	w = must(sql.Open(n, d))
	w.SetMaxOpenConns(1)
	n, d = dsn(path, true)
	r = must(sql.Open(n, d))
	r.SetMaxOpenConns(10)
	var mode string
	check(w.QueryRow("PRAGMA journal_mode").Scan(&mode))
	if mode != "wal" {
		panic("journal_mode=" + mode)
	}
	return w, r
}

func main() {
	flag.Parse()
	if *dir == "" {
		*dir = must(os.MkdirTemp(".", "run-"))
		defer os.RemoveAll(*dir)
	}
	fmt.Printf("# driver=%s fullfsync=%v synchronous=%s scenario=%s\n", *driver, *fullfsync, *syncMode, *scenario)
	path := filepath.Join(*dir, "state.db")
	switch *scenario {
	case "commit":
		runCommit(path)
	case "steady":
		runState(path, false)
	case "burst":
		runState(path, true)
	case "logs":
		runLogs(filepath.Join(*dir, "logs.db"))
	default:
		panic("unknown scenario")
	}
}

// runCommit times 300 one-row transactions: ~4 ms each means F_FULLFSYNC
// reached the drive, ~0.1 ms means it did not.
func runCommit(path string) {
	w, _ := open(path)
	mustExec(w, "CREATE TABLE t(id INTEGER PRIMARY KEY, v BLOB)")
	var lat []time.Duration
	for i := 0; i < 300; i++ {
		t := time.Now()
		mustExec(w, "INSERT INTO t(v) VALUES (?)", randBytes(500))
		lat = append(lat, time.Since(t))
	}
	report("commit_1row", lat)
}

// ---- helpers ----

func report(name string, d []time.Duration) {
	if len(d) == 0 {
		fmt.Printf("%-22s n=0\n", name)
		return
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	q := func(p float64) time.Duration { return d[int(p*float64(len(d)-1))].Round(time.Microsecond) }
	fmt.Printf("%-22s n=%-7d p50=%-10v p99=%-10v max=%v\n", name, len(d), q(0.5), q(0.99), d[len(d)-1].Round(time.Microsecond))
}

var letters = []byte("abcdefghijklmnopqrstuvwxyz0123456789 ─│┌┐└┘$>#")

// randBytes is printable, mostly incompressible text, a pessimistic stand-in
// for a pane summary or a compressed log chunk.
func randBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return b
}

func mustExec(db *sql.DB, q string, args ...any) {
	_, err := db.Exec(q, args...)
	check(err)
}

func must[T any](v T, err error) T {
	check(err)
	return v
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

var blobPool = map[int][][]byte{}

// blob returns a precomputed random blob so generating data is not timed
// inside a transaction.
func blob(n int) []byte {
	p, ok := blobPool[n]
	if !ok {
		for i := 0; i < 1024; i++ {
			p = append(p, randBytes(n))
		}
		blobPool[n] = p
	}
	return p[rand.IntN(len(p))]
}
