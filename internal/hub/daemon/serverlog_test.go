package daemon_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A failed TLS handshake is one debug-level JSON event, not a plain-text line
// from net/http on stderr. Not parallel: it swaps the standard logger's
// output, which is where an http.Server without ErrorLog writes.
func TestABadTLSHandshakeLogsOnlyJSON(t *testing.T) {
	var std bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&std)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	h := newHub(t, "")
	h.tel.SetLogLevel(slog.LevelDebug)
	h.init(t)
	r := h.start(t, h.options())
	id := h.storedIdentity(t)
	if _, err := dialAgent(t, caPool(t, id), "127.0.0.1:"+port(r.addrs.Agent), "127.0.0.1", tls.VersionTLS12); err == nil {
		t.Fatal("a TLS 1.2-capped client connected")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(h.logs.String(), `"msg":"http.server_error"`) {
		time.Sleep(20 * time.Millisecond)
	}
	r.stop(t)

	if std.Len() != 0 {
		t.Errorf("net/http wrote to the standard logger: %q", std.String())
	}
	sc := bufio.NewScanner(strings.NewReader(h.logs.String()))
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			t.Errorf("log line is not JSON: %q", sc.Text())
		}
	}
	events := h.logs.events(t, "http.server_error")
	if len(events) != 1 {
		t.Fatalf("http.server_error lines = %d, want 1 for one bad handshake", len(events))
	}
	e := events[0]
	if e["level"] != "debug" || e["listener"] != "agent" || e["kind"] != "tls_handshake" {
		t.Errorf("http.server_error = %v, want level debug, listener agent, kind tls_handshake", e)
	}
	if strings.Contains(h.logs.String(), "unsupported versions") || strings.Contains(h.logs.String(), "127.0.0.1:") {
		t.Error("the event carries net/http's error text or the peer address")
	}
}
