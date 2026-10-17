package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

const gib = int64(1) << 30

// systemDiskSources evaluates the hub signals from fixed readings: an idle
// writer and 9 GiB free, which is WARNING for DISK_FREE (under 10 GiB).
func systemDiskSources() system.Sources {
	return system.Sources{
		Writer: func(time.Time) (time.Duration, time.Duration) { return 0, 0 },
		DiskFree: func() (int64, bool) {
			return 9 * gib, true
		},
	}
}

// tickSystemLoop runs two evaluations: leaving OK needs two consecutive
// worse readings, so one tick still serves the old state.
func tickSystemLoop(t *testing.T, e *fleetEnv) {
	t.Helper()
	ctx := context.Background()
	if err := e.sysLoop.Run(ctx, fleetNow); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := e.sysLoop.Run(ctx, fleetNow.Add(time.Second)); err != nil {
		t.Fatalf("second Run: %v", err)
	}
}

func scrapeMetrics(t *testing.T, e *fleetEnv) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.tel.MetricsHandler(func(*http.Request) bool { return true }).ServeHTTP(rec,
		httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	return rec.Body.String()
}

// TestSystemSignalGaugesServeLoopVerdicts pins the 05b gauges (issue #41):
// one state series per served (signal, subject) with 0 ok, 1 warning,
// 2 critical, 3 unknown, and one value series carrying the reading behind
// the state (the measure that set it, else MAIN), absent while unknown.
// scrapeSeries finds the scraped line for one (metric, signal, subject)
// series. The exporter sorts its otel_scope_* labels ahead of ours, so the
// match is on the metric head plus our two labels, not the full head.
func scrapeSeries(body, metric, signal, subject string) (string, bool) {
	sig := `signal="` + signal + `"`
	sub := `subject="` + subject + `"`
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, metric+"{") && strings.Contains(line, sig) && strings.Contains(line, sub) {
			return line, true
		}
	}
	return "", false
}

func TestSystemSignalGaugesServeLoopVerdicts(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false), withSystemSources(systemDiskSources()))
	acctID := seedSystemSubjects(t, e)
	tickSystemLoop(t, e)
	body := scrapeMetrics(t, e)
	for _, want := range []struct {
		metric, signal, subject, value string
	}{
		{"fleet_hub_system_signal_state", "disk_free", "hub", "1"},
		{"fleet_hub_system_signal_state", "writer_batch_lag", "hub", "0"},
		{"fleet_hub_system_signal_state", "watch_drops", "hub", "3"},
		{"fleet_hub_system_signal_state", "log_store_usage", "hub", "3"},
		{"fleet_hub_system_signal_state", "backup_age", "hub", "3"},
		{"fleet_hub_system_signal_state", "agent_connected", "m-laptop", "3"},
		{"fleet_hub_system_signal_state", "outbox_backlog", "m-desk", "3"},
		{"fleet_hub_system_signal_state", "clock_skew", "m-desk", "3"},
		{"fleet_hub_system_signal_state", "usage_probe_freshness", acctID, "3"},
		{"fleet_hub_system_signal_state", "integration_sync_lag", "github", "3"},
		{"fleet_hub_system_signal_value", "writer_batch_lag", "hub", "0"},
	} {
		line, ok := scrapeSeries(body, want.metric, want.signal, want.subject)
		if !ok {
			t.Errorf("metrics scrape lacks %s{signal=%q,subject=%q}", want.metric, want.signal, want.subject)
			continue
		}
		if !strings.HasSuffix(line, "} "+want.value) {
			t.Errorf("%s{signal=%q,subject=%q} = %q, want value %q", want.metric, want.signal, want.subject, line, want.value)
		}
	}
	// 9 GiB prints as a float; the head pins the series either way.
	if line, ok := scrapeSeries(body, "fleet_hub_system_signal_value", "disk_free", "hub"); !ok ||
		!strings.Contains(line, "} 9.66") {
		t.Errorf("metrics scrape lacks the disk_free value series (≈9 GiB), got %q", line)
	}
	// Unknown readings stay unknown, never 0: no value series.
	for _, signal := range []string{"watch_drops", "log_store_usage", "backup_age", "agent_connected"} {
		if line, ok := scrapeSeries(body, "fleet_hub_system_signal_value", signal, "hub"); ok {
			t.Errorf("metrics scrape holds value series %q, want none for an unknown reading", line)
		}
	}
}

// TestSystemSignalGaugesReportNothingWhenStoreUnreadable pins the scrape
// failure branch: a store the scrape cannot read reports no series, like
// the work-items gauge; GetSystemStatus's system.status_failed covers the
// diagnosis.
func TestSystemSignalGaugesReportNothingWhenStoreUnreadable(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false), withSystemSources(systemDiskSources()))
	tickSystemLoop(t, e)
	if err := e.st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if body := scrapeMetrics(t, e); strings.Contains(body, "fleet_hub_system_signal_") {
		t.Errorf("metrics scrape holds a system signal series from a closed store")
	}
}
