package main

import (
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// startStatusFile keeps status.json current for the life of a run and
// returns the function that writes it a last time. Call it after the loop
// exists and run the returned function after Run returns (defer it), so the
// last write carries the end the loop stopped on.
//
// The file is written at start, at most once a second after the loop
// signals a change (Loop.StatusChanged, a push source: no timer re-reads the
// loop's state, ADR 0022 rule 6, ADR 0024 decision 9), at least once a
// minute, and at the stop. push_off is not signalled by the loop; it is read
// when the file is written, so it can be up to a minute old.
//
// A run that got this far has loaded an identity, so it has no failed enroll
// to show: it removes enroll.json.
func startStatusFile(dir string, tel *telemetry.Telemetry, loop *conn.Loop) func() {
	_ = statusfile.RecordEnroll(tel, dir, time.Now(), nil)
	w, err := statusfile.Start(statusfile.Options{Dir: dir, Source: loop.Status, Changed: loop.StatusChanged(), Telemetry: tel})
	if err != nil {
		// Only the metric registration can fail here. The agent runs
		// without a status file rather than not at all.
		tel.Logger.Warn("status.write_failed", "component", "run", "file", statusfile.StatusFile, "step", "start", "err_kind", "io")
		return func() {}
	}
	return w.Stop
}

// recordEnroll leaves the outcome of `enroll` in enroll.json: the failure
// code of a failed enroll, or no file after a good one. A refusal because
// the machine is already enrolled changes nothing, and a state folder that
// does not exist is not made (see [statusfile.RecordEnroll]). A write that
// fails is logged by the statusfile package and never changes the exit
// code.
func recordEnroll(tel *telemetry.Telemetry, dir string, enrollErr error) {
	_ = statusfile.RecordEnroll(tel, dir, time.Now(), enrollErr)
}
