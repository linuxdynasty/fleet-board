package telemetry_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func newTelemetry(t *testing.T, cfg telemetry.Config) *telemetry.Telemetry {
	t.Helper()
	tel, err := telemetry.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return tel
}

func hubConfig(out io.Writer) telemetry.Config {
	return telemetry.Config{Service: telemetry.Hub, Version: "v0.0.0-test", InstanceID: "hub-instance-1", LogOutput: out}
}

// scrape serves /metrics to a loopback peer and returns the text body.
func scrape(t *testing.T, tel *telemetry.Telemetry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback scrape: status %d, body %q", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// sample returns the value of the first series of name whose labels contain
// every want (`key="value"`), or -1 when there is none.
func sample(body, name string, want ...string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		ok := true
		for _, w := range want {
			if !strings.Contains(line, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return -1
		}
		return v
	}
	return -1
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  telemetry.Config
	}{
		{"no service", telemetry.Config{Version: "v1", InstanceID: "i"}},
		{"unknown service", telemetry.Config{Service: "fleet-web", Version: "v1", InstanceID: "i"}},
		{"no instance id", telemetry.Config{Service: telemetry.Hub, Version: "v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tel, err := telemetry.New(tc.cfg); err == nil {
				_ = tel.Shutdown(context.Background())
				t.Fatalf("New(%+v) = nil error, want an error", tc.cfg)
			}
		})
	}
}

func TestLoggerWritesOneJSONEventPerLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tel := newTelemetry(t, hubConfig(&buf))
	log := tel.Logger.With("component", "writer")
	log.Debug("store.debug_hidden")
	log.Info("store.opened", "db", "state")
	log.Warn("store.slow")
	log.Error("store.commit_failed", "err_kind", "io")
	tel.SetLogLevel(slog.LevelDebug)
	log.Debug("store.debug_shown")

	lines := decodeLines(t, &buf)
	want := []struct{ msg, level string }{
		{"store.opened", "info"},
		{"store.slow", "warn"},
		{"store.commit_failed", "error"},
		{"store.debug_shown", "debug"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i, w := range want {
		got := lines[i]
		if got["msg"] != w.msg || got["level"] != w.level || got["component"] != "writer" {
			t.Errorf("line %d = %v, want msg %q level %q component writer", i, got, w.msg, w.level)
		}
		ts, _ := got["time"].(string)
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || !strings.HasSuffix(ts, "Z") || parsed.Location() != time.UTC {
			t.Errorf("line %d time %q is not RFC 3339 UTC (err %v)", i, ts, err)
		}
	}
	if lines[0]["db"] != "state" || lines[2]["err_kind"] != "io" {
		t.Errorf("attributes lost: %v", lines)
	}
}

func TestLoggerAddsTraceIDsWhenASpanIsActive(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tel := newTelemetry(t, hubConfig(&buf))
	ctx, span := tel.Tracer("test").Start(context.Background(), "hub.test.op")
	tel.Logger.InfoContext(ctx, "test.inside")
	span.End()
	tel.Logger.InfoContext(context.Background(), "test.outside")

	lines := decodeLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	sc := span.SpanContext()
	if !sc.IsValid() {
		t.Fatal("span has no valid context: the tracer provider records no trace ids")
	}
	if lines[0]["trace_id"] != sc.TraceID().String() || lines[0]["span_id"] != sc.SpanID().String() {
		t.Errorf("inside span: got trace_id %v span_id %v, want %s %s", lines[0]["trace_id"], lines[0]["span_id"], sc.TraceID(), sc.SpanID())
	}
	if _, ok := lines[1]["trace_id"]; ok {
		t.Errorf("outside span: unexpected trace_id in %v", lines[1])
	}
}

func TestLogLinesAreCountedByLevel(t *testing.T) {
	t.Parallel()
	tel := newTelemetry(t, hubConfig(io.Discard))
	tel.Logger.Info("a.one")
	tel.Logger.Info("a.two")
	tel.Logger.Warn("a.three")
	tel.Logger.Debug("a.hidden") // below the level: not written, not counted

	body := scrape(t, tel)
	cases := []struct {
		level string
		want  float64
	}{{"info", 2}, {"warn", 1}, {"debug", -1}}
	for _, tc := range cases {
		if got := sample(body, "fleet_hub_log_lines_total", `level="`+tc.level+`"`); got != tc.want {
			t.Errorf("fleet_hub_log_lines_total{level=%q} = %v, want %v", tc.level, got, tc.want)
		}
	}
}

func TestMetricPrefixAndResourceFollowTheService(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cfg     telemetry.Config
		prefix  string
		service string
	}{
		{"hub", telemetry.Config{Service: telemetry.Hub, Version: "v1.2.3", InstanceID: "h1", LogOutput: io.Discard}, "fleet_hub", `service_name="fleetd"`},
		{"agent", telemetry.Config{Service: telemetry.Agent, Version: "v1.2.3", InstanceID: "boot-1", MachineID: "m-1", LogOutput: io.Discard}, "fleet_agent", `service_name="fleet-agent"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tel := newTelemetry(t, tc.cfg)
			if got := tel.MetricPrefix(); got != tc.prefix {
				t.Errorf("MetricPrefix() = %q, want %q", got, tc.prefix)
			}
			tel.Logger.Info("x.y")
			body := scrape(t, tel)
			if sample(body, tc.prefix+"_log_lines_total", `level="info"`) != 1 {
				t.Errorf("no %s_log_lines_total in scrape:\n%s", tc.prefix, body)
			}
			if sample(body, "target_info", tc.service, `service_version="v1.2.3"`) != 1 {
				t.Errorf("target_info lacks %s and the version:\n%s", tc.service, body)
			}
			if sample(body, "go_goroutines") < 1 {
				t.Errorf("runtime metric go_goroutines missing:\n%s", body)
			}
		})
	}
}

func TestMetricsHandlerAdmitsOnlyLoopbackOrAnAllowedRequest(t *testing.T) {
	t.Parallel()
	tel := newTelemetry(t, hubConfig(io.Discard))
	allowBearer := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer admin" }
	cases := []struct {
		name   string
		remote string
		header map[string]string
		want   int
	}{
		{"loopback v4", "127.0.0.1:41000", nil, http.StatusOK},
		{"loopback v6", "[::1]:41000", nil, http.StatusOK},
		{"remote", "192.168.1.20:41000", nil, http.StatusUnauthorized},
		{"remote with forwarded-for loopback", "192.168.1.20:41000", map[string]string{"X-Forwarded-For": "127.0.0.1"}, http.StatusUnauthorized},
		{"remote with a cookie", "192.168.1.20:41000", map[string]string{"Cookie": "fleet_session=abc"}, http.StatusUnauthorized},
		{"remote allowed by the caller's check", "192.168.1.20:41000", map[string]string{"Authorization": "Bearer admin"}, http.StatusOK},
		{"unparseable peer", "not-an-address", nil, http.StatusUnauthorized},
	}
	h := tel.MetricsHandler(allowBearer)
	refused := 0
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = tc.remote
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, rec.Code, tc.want)
		}
		if tc.want != http.StatusOK {
			refused++
			if rec.Body.Len() != 0 {
				t.Errorf("%s: refusal has a body %q", tc.name, rec.Body.String())
			}
		}
	}
	body := scrape(t, tel)
	if got := sample(body, "fleet_hub_auth_failures_total", `reason="metrics"`); got != float64(refused) {
		t.Errorf("fleet_hub_auth_failures_total{reason=metrics} = %v, want %d", got, refused)
	}
}
