package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Run is one browse: one QueryContext call with one timeout.
type Run struct {
	Event        string   `json:"event"`
	TS           string   `json:"ts"`
	Mode         string   `json:"mode"`
	Run          int      `json:"run"`
	TimeoutMS    int64    `json:"timeout_ms"`
	Instance     string   `json:"instance,omitempty"`
	Found        bool     `json:"found"`
	FirstMatchMS *float64 `json:"first_match_ms,omitempty"`
	FirstAnyMS   *float64 `json:"first_any_ms,omitempty"`
	ElapsedMS    float64  `json:"elapsed_ms"`
	EntryCount   int      `json:"entry_count"`
	Entries      []Entry  `json:"entries,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Entry is a copy of one ServiceEntry, taken when it arrives.
type Entry struct {
	AtMS   float64  `json:"at_ms"`
	Name   string   `json:"name"`
	Host   string   `json:"host"`
	AddrV4 string   `json:"addr_v4,omitempty"`
	AddrV6 string   `json:"addr_v6,omitempty"`
	Port   int      `json:"port"`
	TXT    []string `json:"txt,omitempty"`
}

// Dist is a time-to-first-answer distribution in milliseconds.
type Dist struct {
	N   int     `json:"n"`
	Min float64 `json:"min"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// Summary aggregates the runs that share one timeout.
type Summary struct {
	TimeoutMS int64   `json:"timeout_ms"`
	Runs      int     `json:"runs"`
	Found     int     `json:"found"`
	FoundRate float64 `json:"found_rate"`
	Errors    int     `json:"errors"`
	FirstMS   Dist    `json:"first_match_ms"`
}

// percentile uses the nearest-rank method on an ascending slice.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(n))) - 1
	idx = max(idx, 0)
	idx = min(idx, n-1)
	return sorted[idx]
}

// Summarize groups runs by timeout, ascending.
func Summarize(runs []Run) []Summary {
	byTimeout := map[int64][]Run{}
	for _, r := range runs {
		byTimeout[r.TimeoutMS] = append(byTimeout[r.TimeoutMS], r)
	}
	timeouts := make([]int64, 0, len(byTimeout))
	for t := range byTimeout {
		timeouts = append(timeouts, t)
	}
	sort.Slice(timeouts, func(i, j int) bool { return timeouts[i] < timeouts[j] })

	var out []Summary
	for _, t := range timeouts {
		group := byTimeout[t]
		s := Summary{TimeoutMS: t, Runs: len(group)}
		var firsts []float64
		for _, r := range group {
			if r.Found {
				s.Found++
				if r.FirstMatchMS != nil {
					firsts = append(firsts, *r.FirstMatchMS)
				}
			}
			if r.Error != "" {
				s.Errors++
			}
		}
		s.FoundRate = float64(s.Found) / float64(s.Runs)
		if len(firsts) > 0 {
			sort.Float64s(firsts)
			s.FirstMS = Dist{
				N:   len(firsts),
				Min: firsts[0],
				P50: percentile(firsts, 50),
				P90: percentile(firsts, 90),
				P99: percentile(firsts, 99),
				Max: firsts[len(firsts)-1],
			}
		}
		out = append(out, s)
	}
	return out
}

// readRuns returns the browse_run records in a JSON-line stream and how many
// lines were not JSON.
func readRuns(r io.Reader) ([]Run, int, error) {
	var runs []Run
	skipped := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var run Run
		if err := json.Unmarshal(sc.Bytes(), &run); err != nil {
			skipped++
			continue
		}
		if run.Event == "browse_run" {
			runs = append(runs, run)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, fmt.Errorf("scan: %w", err)
	}
	return runs, skipped, nil
}

func runSummarize(args []string) error {
	fs := flag.NewFlagSet("summarize", flag.ContinueOnError)
	table := fs.Bool("table", false, "print a markdown table instead of JSON lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var all []Run
	skipped := 0
	files := fs.Args()
	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, name := range files {
		var in io.Reader = os.Stdin
		if name != "-" {
			f, err := os.Open(name)
			if err != nil {
				return fmt.Errorf("open %s: %w", name, err)
			}
			defer f.Close()
			in = f
		}
		runs, n, err := readRuns(in)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		all = append(all, runs...)
		skipped += n
	}
	sums := Summarize(all)
	if *table {
		fmt.Println("| timeout | runs | found | rate | errors | first answer ms: min | p50 | p90 | p99 | max |")
		fmt.Println("|---|---|---|---|---|---|---|---|---|---|")
		for _, s := range sums {
			d := s.FirstMS
			fmt.Printf("| %d ms | %d | %d | %.2f | %d | %.1f | %.1f | %.1f | %.1f | %.1f |\n",
				s.TimeoutMS, s.Runs, s.Found, s.FoundRate, s.Errors, d.Min, d.P50, d.P90, d.P99, d.Max)
		}
		fmt.Printf("\nnon-JSON lines skipped: %d\n", skipped)
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	for _, s := range sums {
		if err := enc.Encode(s); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
	}
	return nil
}
