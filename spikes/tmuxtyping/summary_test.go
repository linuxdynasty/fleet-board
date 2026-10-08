package main

import (
	"strings"
	"testing"
)

func TestStats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		in          []float64
		lo, med, hi float64
		ok          bool
	}{
		{"empty", nil, 0, 0, 0, false},
		{"one", []float64{7}, 7, 7, 7, true},
		{"odd unsorted", []float64{900, 100, 500}, 100, 500, 900, true},
		{"even takes mean of middle", []float64{400, 100, 200, 1000}, 100, 300, 1000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lo, med, hi, ok := stats(tc.in)
			if lo != tc.lo || med != tc.med || hi != tc.hi || ok != tc.ok {
				t.Fatalf("stats(%v) = %v %v %v %v, want %v %v %v %v", tc.in, lo, med, hi, ok, tc.lo, tc.med, tc.hi, tc.ok)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	recs := []trialRec{
		{Event: "setup"},
		{
			Event: "trial", Tmux: "tmux 3.2a", Kind: "type-work", Pane: "%0",
			SubLat:  map[string]float64{"sess_act": 400},
			OutLat:  map[string]float64{"%0": 2, "%1": 9},
			PollSaw: &yes,
		},
		{
			Event: "trial", Tmux: "tmux 3.2a", Kind: "type-work", Pane: "%0",
			SubLat:  map[string]float64{"sess_act": 1000},
			OutLat:  map[string]float64{"%0": 4},
			PollSaw: &no,
		},
		{Event: "trial", Tmux: "tmux 3.2a", Kind: "idle", Pane: "%0"},
	}
	got := summarize(recs)
	for _, want := range []string{
		"| tmux 3.2a | type-work | %output of the pane | 2/2 | 2 | 3 | 4 |",
		"| tmux 3.2a | type-work | sub sess_act | 2/2 | 400 | 700 | 1000 |",
		"| tmux 3.2a | type-work | list-clients poll saw it | 1/2 | | | |",
		"| tmux 3.2a | idle | (nothing) | 0/1 | | | |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q\n%s", want, got)
		}
	}
}
