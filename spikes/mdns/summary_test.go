package main

import (
	"strings"
	"testing"
)

func ms(v float64) *float64 { return &v }

func TestPercentile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		sorted []float64
		p      float64
		want   float64
	}{
		{"empty", nil, 50, 0},
		{"single", []float64{7}, 99, 7},
		{"p50 of 1..10 nearest rank", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 50, 5},
		{"p90 of 1..10", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 90, 9},
		{"p99 of 1..10 is max", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 99, 10},
		{"p0 is min", []float64{3, 4}, 0, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := percentile(tc.sorted, tc.p); got != tc.want {
				t.Fatalf("percentile(%v, %v) = %v, want %v", tc.sorted, tc.p, got, tc.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		runs []Run
		want []Summary
	}{
		{name: "no runs", runs: nil, want: nil},
		{
			name: "groups by timeout, sorted, misses and errors counted",
			runs: []Run{
				{TimeoutMS: 2000, Found: true, FirstMatchMS: ms(4)},
				{TimeoutMS: 1000, Found: true, FirstMatchMS: ms(30)},
				{TimeoutMS: 1000, Found: false, Error: "boom"},
				{TimeoutMS: 1000, Found: true, FirstMatchMS: ms(10)},
				{TimeoutMS: 1000, Found: true, FirstMatchMS: ms(20)},
			},
			want: []Summary{
				{TimeoutMS: 1000, Runs: 4, Found: 3, FoundRate: 0.75, Errors: 1, FirstMS: Dist{N: 3, Min: 10, P50: 20, P90: 30, P99: 30, Max: 30}},
				{TimeoutMS: 2000, Runs: 1, Found: 1, FoundRate: 1, FirstMS: Dist{N: 1, Min: 4, P50: 4, P90: 4, P99: 4, Max: 4}},
			},
		},
		{
			name: "found without a timing does not enter the distribution",
			runs: []Run{{TimeoutMS: 3000, Found: true}},
			want: []Summary{{TimeoutMS: 3000, Runs: 1, Found: 1, FoundRate: 1}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Summarize(tc.runs)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d summaries %+v, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("summary %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestReadRuns(t *testing.T) {
	t.Parallel()
	in := strings.Join([]string{
		`{"event":"start","mode":"browse"}`,
		`{"event":"browse_run","run":1,"timeout_ms":1000,"found":true,"first_match_ms":3.5}`,
		`not json at all`,
		`{"event":"browse_run","run":2,"timeout_ms":1000,"found":false,"error":"x"}`,
		`{"event":"summary"}`,
	}, "\n")
	runs, skipped, err := readRuns(strings.NewReader(in))
	if err != nil {
		t.Fatalf("readRuns: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the non-JSON line)", skipped)
	}
	if len(runs) != 2 || runs[0].Run != 1 || *runs[0].FirstMatchMS != 3.5 || runs[1].Error != "x" {
		t.Fatalf("runs = %+v", runs)
	}
}
