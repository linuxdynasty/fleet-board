package telemetry_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestHTTPErrorLogWritesOneEventWithAClosedKindAndNoText(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, line, kind, level string }{
		{"handshake", "http: TLS handshake error from 192.0.2.7:51234: tls: client offered only unsupported versions: [303]", "tls_handshake", "debug"},
		{"other", "http2: server: error reading preface from client 192.0.2.7:51234: bogus greeting \"secret-ish\"", "other", "debug"},
		{"panic", "http: panic serving 192.0.2.7:51234: secret-ish\ngoroutine 7 [running]:\nnet/http.(*conn).serve.func1()", "panic", "error"},
		{"http2 panic", "http2: panic serving 192.0.2.7:51234: secret-ish\ngoroutine 9 [running]:\nnet/http/internal/http2.(*serverConn).runHandler.func1()", "panic", "error"},
		{"peer bytes name a panic", "http2: server: error reading preface from client 192.0.2.7:51234: bogus greeting \"http: panic serving x\"", "other", "debug"},
		{"peer bytes name a handshake error", "http2: server: error reading preface from client 192.0.2.7:51234: bogus greeting \"TLS handshake error\"", "other", "debug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			tel := newTelemetry(t, hubConfig(&out))
			tel.SetLogLevel(slog.LevelDebug)
			tel.HTTPErrorLog("agent").Print(tc.line)

			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("got %d log lines, want 1: %q", len(lines), out.String())
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
				t.Fatalf("not JSON: %q", lines[0])
			}
			if m["msg"] != "http.server_error" || m["level"] != tc.level || m["listener"] != "agent" || m["kind"] != tc.kind {
				t.Errorf("event %v, want http.server_error %s agent %s", m, tc.level, tc.kind)
			}
			if s := out.String(); strings.Contains(s, "192.0.2.7") || strings.Contains(s, "secret-ish") || strings.Contains(s, "goroutine") {
				t.Error("the event carries net/http's text")
			}
		})
	}
}

func TestHTTPErrorLogIsSilentAtTheDefaultLevel(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	newTelemetry(t, hubConfig(&out)).HTTPErrorLog("api").Print("http: TLS handshake error from 192.0.2.7:1: EOF")
	if out.Len() != 0 {
		t.Errorf("logged at the default info level: %q", out.String())
	}
}
