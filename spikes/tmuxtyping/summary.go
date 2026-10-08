package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// trialRec is the part of a "trial" JSON line the summary reads.
type trialRec struct {
	Event     string             `json:"event"`
	Tmux      string             `json:"tmux"`
	Kind      string             `json:"kind"`
	Pane      string             `json:"pane"`
	SubLat    map[string]float64 `json:"sub_latency_ms"`
	OutLat    map[string]float64 `json:"output_latency_ms"`
	PollSaw   *bool              `json:"poll_saw_input"`
	OtherNote []string           `json:"other_notifications"`
}

// stats returns min, median and max; the median of an even count is the mean
// of the two middle values. ok is false for no values.
func stats(v []float64) (lo, med, hi float64, ok bool) {
	if len(v) == 0 {
		return 0, 0, 0, false
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	med = s[n/2]
	if n%2 == 0 {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	return s[0], med, s[n-1], true
}

// summarize groups trials by tmux version and kind and, for every signal,
// counts in how many trials it arrived and how fast.
func summarize(recs []trialRec) string {
	type key struct{ tmux, kind string }
	groups := map[key][]trialRec{}
	var keys []key
	for _, r := range recs {
		if r.Event != "trial" {
			continue
		}
		k := key{r.Tmux, r.Kind}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], r)
	}
	var b strings.Builder
	b.WriteString("| tmux | trial | signal | arrived | min ms | median ms | max ms |\n|---|---|---|---|---|---|---|\n")
	for _, k := range keys {
		rs := groups[k]
		lat := map[string][]float64{}
		var names []string
		add := func(name string, ms float64) {
			if _, ok := lat[name]; !ok {
				names = append(names, name)
			}
			lat[name] = append(lat[name], ms)
		}
		polled := 0
		for _, r := range rs {
			for n, ms := range r.SubLat {
				add("sub "+n, ms)
			}
			if ms, ok := r.OutLat[r.Pane]; ok {
				add("%output of the pane", ms)
			}
			for _, n := range r.OtherNote {
				add("other %"+n, 0)
			}
			if r.PollSaw != nil && *r.PollSaw {
				polled++
			}
		}
		sort.Strings(names)
		for _, n := range names {
			lo, med, hi, _ := stats(lat[n])
			fmt.Fprintf(&b, "| %s | %s | %s | %d/%d | %.0f | %.0f | %.0f |\n",
				k.tmux, k.kind, n, len(lat[n]), len(rs), lo, med, hi)
		}
		if len(names) == 0 {
			fmt.Fprintf(&b, "| %s | %s | (nothing) | 0/%d | | | |\n", k.tmux, k.kind, len(rs))
		}
		if rs[0].PollSaw != nil {
			fmt.Fprintf(&b, "| %s | %s | list-clients poll saw it | %d/%d | | | |\n", k.tmux, k.kind, polled, len(rs))
		}
	}
	return b.String()
}

func runSummarize(w io.Writer, files []string) error {
	var recs []trialRec
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r trialRec
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				recs = append(recs, r)
			}
		}
		_ = fh.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	_, err := io.WriteString(w, summarize(recs))
	return err
}
