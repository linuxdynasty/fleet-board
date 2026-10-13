package main

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The cold-cache run fills the file in one process and measures in another
// (04 "Not proven"). The handoff is the session count: the measuring process
// must see exactly the sessions the filling process wrote, and must refuse a
// file that was never prefilled.
func TestLogsReuseHandoff(t *testing.T) {
	oldPrefill := *prefill
	*prefill = 2 * chunksPerSess // two whole one-hour sessions, by hand
	defer func() { *prefill = oldPrefill }()

	dir := t.TempDir()

	// The filling process writes its sessions.
	filled := filepath.Join(dir, "logs.db")
	w, _ := open(filled)
	createLogsSchema(w)
	if got := prefillLogs(w, filled); got != 2 {
		t.Fatalf("prefillLogs sessions = %d, want 2", got)
	}

	// The measuring process reopens the same file and sees both sessions.
	w2, _ := open(filled)
	if got, err := logsSessionCount(w2); err != nil || got != 2 {
		t.Fatalf("logsSessionCount(filled) = %d, %v; want 2, nil", got, err)
	}

	// Anything else is refused instead of measured.
	cases := []struct {
		name    string
		prepare func(p string)
	}{
		{"missing schema", func(p string) { _, _ = open(p) }},
		{"schema without sessions", func(p string) {
			w3, _ := open(p)
			createLogsSchema(w3)
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, "case"+strconv.Itoa(i)+".db")
			tc.prepare(p)
			w4, _ := open(p)
			if got, err := logsSessionCount(w4); err == nil {
				t.Fatalf("logsSessionCount = %d, nil; want an error", got)
			}
		})
	}
}

// The measuring half must run to completion on the file the filling half
// wrote: it derives a zero session count when the handoff is wrong, and the
// replay readers panic on it.
func TestLogsReuseMeasures(t *testing.T) {
	oldPrefill, oldDur, oldReuse := *prefill, *dur, *reuse
	*prefill = chunksPerSess // one whole one-hour session, by hand
	*dur = 1500 * time.Millisecond
	*reuse = true
	defer func() { *prefill, *dur, *reuse = oldPrefill, oldDur, oldReuse }()

	dir := t.TempDir()
	path := filepath.Join(dir, "logs.db")
	w, _ := open(path)
	createLogsSchema(w)
	if got := prefillLogs(w, path); got != 1 {
		t.Fatalf("prefillLogs sessions = %d, want 1", got)
	}
	runLogs(path)

	w2, _ := open(path)
	if got, err := logsSessionCount(w2); err != nil || got < 1 {
		t.Fatalf("logsSessionCount after measure = %d, %v; want >= 1, nil", got, err)
	}
}
