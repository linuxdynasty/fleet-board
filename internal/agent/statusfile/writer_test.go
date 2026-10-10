package statusfile

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func TestStartWritesAtOnceAndFloorsAtAMinute(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if got := r.read(t); got.State != "discovering" || !got.WrittenTime.Equal(t0) {
		t.Fatalf("first file = %+v", got)
	}
	r.clock.wantTimer(t, time.Minute)

	r.setState(conn.StateConnected) // no Notify: only the floor sees it
	r.clock.Advance(59 * time.Second)
	r.noWrite(t)
	r.clock.Advance(time.Second)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	got := r.read(t)
	if got.State != "connected" || !got.WrittenTime.Equal(t0.Add(time.Minute)) {
		t.Errorf("floor write = %+v", got)
	}
	r.clock.wantTimer(t, time.Minute) // a new minute starts after each write
}

func TestChangesAreThrottledToOneWritePerSecond(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)

	r.setState(conn.StateConnecting)
	r.w.Notify()
	r.clock.wantTimer(t, time.Second) // the write waits out the rest of the second
	r.setState(conn.StateHelloSent)
	r.w.Notify()
	r.w.Notify()
	r.clock.Advance(500 * time.Millisecond)
	r.noWrite(t)
	r.clock.Advance(500 * time.Millisecond)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if got := r.read(t); got.State != "hello_sent" || !got.WrittenTime.Equal(t0.Add(time.Second)) {
		t.Errorf("throttled write = %+v, want the latest state at t+1s", got)
	}
	r.clock.wantTimer(t, time.Minute)
	r.noWrite(t) // the three notifies made one write
}

func TestAChangeAfterAQuietSecondIsWrittenAtOnce(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	r.clock.Advance(1500 * time.Millisecond)
	r.setState(conn.StateConnected)
	r.w.Notify()
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if got := r.read(t); got.State != "connected" {
		t.Errorf("state = %q", got.State)
	}
	r.clock.wantTimer(t, time.Minute)
}

func TestAChangeDuringTheWaitDoesNotMoveTheFloorOrWriteTwice(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	r.clock.Advance(400 * time.Millisecond)
	r.w.Notify()
	r.clock.wantTimer(t, 600*time.Millisecond) // only what is left of the second
	r.clock.Advance(600 * time.Millisecond)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	r.noWrite(t)
}

func TestStopWritesOnceMoreWithTheFinalState(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	// The loop ended on its own after the last write; Notify was never called.
	r.setState(conn.StateEnded)
	r.setEnded(conn.EndRevoked)
	r.w.Stop()
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	got := r.read(t)
	if got.State != "ended" || got.Ended != "revoked" {
		t.Errorf("final file = %+v, want the ended state", got)
	}
	r.w.Stop() // idempotent
	r.w.Notify()
	r.noWrite(t)
}

func TestStopFlushesAPendingChangeOnce(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	r.setState(conn.StateConnected)
	r.w.Notify()
	r.clock.wantTimer(t, time.Second)
	r.w.Stop()
	_ = r.waitWrite(t)
	r.noWrite(t)
	if got := r.read(t); got.State != "connected" {
		t.Errorf("state = %q", got.State)
	}
}

func TestAFailedWriteKeepsTheOldFileCountsAndLogsOncePerMinute(t *testing.T) {
	t.Parallel()
	r, logs := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	boom := errors.New("disk is on fire")
	r.setRename(func(string, string) error { return boom })
	r.setState(conn.StateConnected)
	for range 3 {
		r.clock.Advance(20 * time.Second)
		r.w.Notify()
		if err := r.waitWrite(t); !errors.Is(err, boom) {
			t.Fatalf("write error = %v, want the rename failure", err)
		}
		r.clock.wantTimer(t, time.Minute)
	}
	if got := r.read(t); got.State != "discovering" {
		t.Errorf("state = %q, want the old file whole", got.State)
	}
	if n := strings.Count(logs.String(), `"msg":"status.write_failed"`); n != 1 {
		t.Errorf("%d status.write_failed lines in 60 s of failures, want 1:\n%s", n, logs.String())
	}
	line := lineOf(t, logs.String(), "status.write_failed")
	for _, want := range []string{`"file":"status.json"`, `"step":"rename"`, `"err_kind":"io"`} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %s: %s", want, line)
		}
	}
	if strings.Contains(logs.String(), "on fire") || strings.Contains(logs.String(), r.dir) {
		t.Errorf("the log carries the error text or the path:\n%s", logs.String())
	}

	r.clock.Advance(20 * time.Second) // 60 s since the first line
	r.w.Notify()
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	if n := strings.Count(logs.String(), `"msg":"status.write_failed"`); n != 2 {
		t.Errorf("%d lines after a minute, want 2", n)
	}

	r.setRename(os.Rename)
	r.clock.Advance(2 * time.Second)
	r.w.Notify()
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"status.write_recovered"`) {
		t.Errorf("no status.write_recovered line:\n%s", logs.String())
	}
	if got := r.read(t); got.State != "connected" {
		t.Errorf("state after recovery = %q", got.State)
	}
}

func lineOf(t *testing.T, logs, msg string) string {
	t.Helper()
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"msg":"`+msg+`"`) {
			return l
		}
	}
	t.Fatalf("no %s line in:\n%s", msg, logs)
	return ""
}

func metricText(t *testing.T, tel *telemetry.Telemetry) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestWritesAreCountedByFileAndResult(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	r.setRename(func(string, string) error { return errors.New("x") })
	r.clock.Advance(time.Minute)
	_ = r.waitWrite(t)
	text := metricText(t, r.tel)
	for result, want := range map[string]string{"ok": "1", "failed": "1"} {
		got := sample(text, "fleet_agent_status_writes_total", `file="status.json"`, `result="`+result+`"`)
		if got != want {
			t.Errorf("writes_total{result=%s} = %q, want %s:\n%s", result, got, want, text)
		}
	}
}

// sample is the value of the sample of name that carries every label.
func sample(text, name string, labels ...string) string {
	for line := range strings.Lines(text) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), name+"{")
		if !ok {
			continue
		}
		set, value, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		all := true
		for _, l := range labels {
			all = all && strings.Contains(","+set+",", ","+l+",")
		}
		if all {
			return value
		}
	}
	return ""
}

func TestNotifyAndStopRace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "t", InstanceID: "b", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	w, err := Start(Options{Dir: dir, Telemetry: tel, Source: func() conn.Status { return conn.Status{State: conn.StateConnected} }})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				w.Notify()
			}
		})
	}
	wg.Go(w.Stop)
	wg.Go(w.Stop)
	wg.Wait()
	if got, err := Read(dir); err != nil || got.State != "connected" {
		t.Errorf("read = %+v, %v", got, err)
	}
}

func TestStartNeedsItsInputs(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]Options{
		"no dir":       {Source: func() conn.Status { return conn.Status{} }},
		"no source":    {Dir: "x"},
		"no telemetry": {Dir: "x", Source: func() conn.Status { return conn.Status{} }},
	} {
		if _, err := Start(o); err == nil {
			t.Errorf("%s: Start succeeded", name)
		}
	}
}

func TestStatusFileIsPrivate(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	fi, err := os.Stat(filepath.Join(r.dir, StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o", fi.Mode().Perm())
	}
}

// A change signalled on Options.Changed (the loop's StatusChanged, once
// plan row 6 PR 6a lands) is throttled and written like a Notify.
func TestAChangeOnTheChangedChannelIsWritten(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	r.setState(conn.StateConnected)
	select {
	case r.push <- struct{}{}:
	case <-time.After(20 * time.Second):
		t.Fatal("the writer does not read Options.Changed")
	}
	r.clock.wantTimer(t, time.Second)
	r.clock.Advance(time.Second)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if got := r.read(t); got.State != "connected" {
		t.Errorf("state = %q", got.State)
	}
}

// A last write that stalls (a hung disk) does not hold up the agent's
// exit: Stop gives up after five seconds and says so.
func TestStopGivesUpOnAStalledLastWrite(t *testing.T) {
	t.Parallel()
	r, logs := newRig(t)
	_ = r.waitWrite(t)
	r.clock.wantTimer(t, time.Minute)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock) // runs before the rig's own Stop
	r.mu.Lock()
	r.block = release
	r.mu.Unlock()
	stopped := make(chan struct{})
	go func() { r.w.Stop(); close(stopped) }()
	_ = r.waitWrite(t) // the final write is under way and stuck
	r.clock.wantTimer(t, 5*time.Second)
	select {
	case <-stopped:
		t.Fatal("Stop returned before its wait ran out")
	default:
	}
	r.clock.Advance(5 * time.Second)
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("Stop is still waiting after its five seconds")
	}
	if !strings.Contains(logs.String(), `"msg":"status.stop_timeout"`) {
		t.Errorf("no status.stop_timeout line:\n%s", logs.String())
	}
	unblock()
}
