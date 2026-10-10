package housekeeping_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/housekeeping"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }

func (l *logBuf) events(name string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(l.b.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

func newTel(t *testing.T) (*telemetry.Telemetry, *logBuf) {
	t.Helper()
	logs := &logBuf{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel, logs
}

func scrape(t *testing.T, tel *telemetry.Telemetry) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

// recorder is a job that records the clock at each run.
type recorder struct {
	mu   sync.Mutex
	runs []time.Time
}

func (r *recorder) run(_ context.Context, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, now)
	return nil
}

func (r *recorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.runs) }

// Each job runs on its own cadence: at the first check, then once its
// interval has passed since its last run, by the injected clock.
func TestEachJobRunsOnItsOwnCadence(t *testing.T) {
	t.Parallel()
	tel, _ := newTel(t)
	clk := &clock{now: t0}
	fast, slow := &recorder{}, &recorder{}
	hk, err := housekeeping.New(housekeeping.Options{Now: clk.Now, Telemetry: tel},
		housekeeping.Job{Name: "fast", Every: 5 * time.Second, Run: fast.run},
		housekeeping.Job{Name: "slow", Every: time.Minute, Run: slow.run})
	if err != nil {
		t.Fatal(err)
	}
	for range 13 { // t0, +5 s, ..., +60 s
		hk.Check(context.Background())
		clk.add(5 * time.Second)
	}
	if fast.count() != 13 || slow.count() != 2 {
		t.Errorf("fast ran %d times, slow %d; want 13 and 2", fast.count(), slow.count())
	}
	if !slow.runs[1].Equal(t0.Add(time.Minute)) {
		t.Errorf("slow's second run at %v, want t0 + 1 min", slow.runs[1])
	}
}

// A job that fails or panics is logged and counted, and every other job
// still runs, on this check and the next.
func TestAFailingJobNeverStopsTheOthers(t *testing.T) {
	t.Parallel()
	tel, logs := newTel(t)
	clk := &clock{now: t0}
	before, after := &recorder{}, &recorder{}
	hk, err := housekeeping.New(housekeeping.Options{Now: clk.Now, Telemetry: tel},
		housekeeping.Job{Name: "before", Every: time.Second, Run: before.run},
		housekeeping.Job{Name: "fails", Every: time.Second, Run: func(context.Context, time.Time) error {
			return errors.New("store busy")
		}},
		housekeeping.Job{Name: "panics", Every: time.Second, Run: func(context.Context, time.Time) error { panic("bug") }},
		housekeeping.Job{Name: "after", Every: time.Second, Run: after.run})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		hk.Check(context.Background())
		clk.add(time.Second)
	}
	if before.count() != 2 || after.count() != 2 {
		t.Errorf("before ran %d, after %d; want 2 each", before.count(), after.count())
	}
	lines := logs.events("hub.housekeeping_failed")
	kinds := map[string]int{}
	for _, l := range lines {
		kinds[fmt.Sprint(l["job"], "/", l["err_kind"])]++
	}
	if len(lines) != 4 || kinds["fails/io"] != 2 || kinds["panics/panic"] != 2 {
		t.Errorf("hub.housekeeping_failed lines %v", lines)
	}
	for _, l := range lines {
		if l["job"] != "panics" {
			continue
		}
		stack, _ := l["stack"].(string)
		if l["panic"] != "string" || !strings.Contains(stack, "housekeeping_test") || len(stack) > 4096 {
			t.Errorf("a panic's line has panic %v and a %d-byte stack, want its type and a bounded stack naming the job", l["panic"], len(stack))
		}
	}
	body := scrape(t, tel)
	for _, job := range []string{"fails", "panics"} {
		if !metricIs(body, "fleet_hub_housekeeping_failures_total", `job="`+job+`"`, "2") {
			t.Errorf("fleet_hub_housekeeping_failures_total{job=%s} is not 2:\n%s", job, body)
		}
	}
}

func metricIs(body, name, label, value string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, name+"{") && strings.Contains(line, label) && strings.HasSuffix(line, " "+value) {
			return true
		}
	}
	return false
}

// Run checks once per tick and returns when its context ends.
func TestRunChecksOnEachTickAndStops(t *testing.T) {
	t.Parallel()
	tel, _ := newTel(t)
	clk := &clock{now: t0}
	ran := make(chan time.Time, 4)
	hk, err := housekeeping.New(housekeeping.Options{Now: clk.Now, Telemetry: tel},
		housekeeping.Job{Name: "j", Every: time.Second, Run: func(_ context.Context, now time.Time) error { ran <- now; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { defer close(done); hk.Run(ctx, ticks) }()
	for i := range 2 {
		ticks <- time.Time{}
		select {
		case got := <-ran:
			if want := t0.Add(time.Duration(i) * time.Second); !got.Equal(want) {
				t.Errorf("run %d at %v, want %v", i, got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no run after a tick")
		}
		clk.add(time.Second)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

// A clock that stepped back runs the job again rather than waiting for the
// clock to catch up with the last run.
func TestAClockSteppedBackDoesNotStallAJob(t *testing.T) {
	t.Parallel()
	tel, _ := newTel(t)
	clk := &clock{now: t0}
	r := &recorder{}
	hk, err := housekeeping.New(housekeeping.Options{Now: clk.Now, Telemetry: tel},
		housekeeping.Job{Name: "j", Every: time.Minute, Run: r.run})
	if err != nil {
		t.Fatal(err)
	}
	hk.Check(context.Background())
	clk.add(-time.Hour)
	hk.Check(context.Background())
	if r.count() != 2 {
		t.Errorf("%d runs, want 2", r.count())
	}
}

// New refuses a job without a name, a run function or a positive interval,
// and two jobs with one name.
func TestNewRefusesABadJob(t *testing.T) {
	t.Parallel()
	tel, _ := newTel(t)
	ok := func(context.Context, time.Time) error { return nil }
	for name, jobs := range map[string][]housekeeping.Job{
		"no name":     {{Every: time.Second, Run: ok}},
		"no run":      {{Name: "j", Every: time.Second}},
		"no interval": {{Name: "j", Run: ok}},
		"duplicate":   {{Name: "j", Every: time.Second, Run: ok}, {Name: "j", Every: time.Minute, Run: ok}},
	} {
		if _, err := housekeeping.New(housekeeping.Options{Telemetry: tel}, jobs...); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}

// A job that ends because the hub is stopping (its context ended) is not
// a failure: nothing is logged or counted.
func TestAJobStoppedByShutdownIsNotAFailure(t *testing.T) {
	t.Parallel()
	tel, logs := newTel(t)
	ctx, cancel := context.WithCancel(context.Background())
	hk, err := housekeeping.New(housekeeping.Options{Now: (&clock{now: t0}).Now, Telemetry: tel},
		housekeeping.Job{Name: "j", Every: time.Second, Run: func(ctx context.Context, _ time.Time) error {
			cancel()
			return fmt.Errorf("store write: %w", ctx.Err())
		}})
	if err != nil {
		t.Fatal(err)
	}
	hk.Check(ctx)
	if lines := logs.events("hub.housekeeping_failed"); len(lines) != 0 {
		t.Errorf("hub.housekeeping_failed lines %v at shutdown, want none", lines)
	}
	if metricIs(scrape(t, tel), "fleet_hub_housekeeping_failures_total", `job="j"`, "1") {
		t.Error("a shutdown counted as a failure")
	}
}
