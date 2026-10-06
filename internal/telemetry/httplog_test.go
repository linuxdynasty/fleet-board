package telemetry_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestHTTPErrorLogWritesOneDebugEventWithAClosedKindAndNoText(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, line, kind string }{
		{"handshake", "http: TLS handshake error from 192.0.2.7:51234: tls: client offered only unsupported versions: [303]", "tls_handshake"},
		{"other", "http2: server: error reading preface from client 192.0.2.7:51234: bogus greeting \"secret-ish\"", "other"},
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
			if m["msg"] != "http.server_error" || m["level"] != "debug" || m["listener"] != "agent" || m["kind"] != tc.kind {
				t.Errorf("event %v, want http.server_error debug agent %s", m, tc.kind)
			}
			if strings.Contains(out.String(), "192.0.2.7") || strings.Contains(out.String(), "secret-ish") {
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
