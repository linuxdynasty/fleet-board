package typing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func scrape(t *testing.T, tel *telemetry.Telemetry) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: status %d", rec.Code)
	}
	return rec.Body.String()
}

// series returns the value of the one series named name whose labels hold
// every want, or "" when there is none.
func series(body, name string, want ...string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if ok {
			return line[strings.LastIndexByte(line, ' ')+1:]
		}
	}
	return ""
}

// Every refusal is counted by reason and logged as typing.refused with the
// reason and purpose, and never the text or the pane it was given; a line
// that is built is neither counted nor logged.
func TestRefusalsAreCountedAndLoggedWithoutTheText(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-1", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	rec, err := typing.NewRecorder(tel)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := typing.NewLiteral(rec, pk("%1"), typing.PurposeGoalStart, "fine text"); err != nil {
		t.Fatal(err)
	}
	const marker = "MARKER-TEXT-42"
	_, _ = typing.NewLiteral(rec, pk("%1"), typing.PurposeOperatorText, marker+"\nkill-server")
	_, _ = typing.NewLiteral(rec, pk("%1"), typing.PurposeOperatorText, marker+"\r")
	_, _ = typing.NewLiteral(rec, pk("MARKER-PANE"), typing.PurposeProbeCommand, marker)
	_, _ = typing.NewKeys(rec, pk("%1"), typing.PurposePromptAnswer)

	body := scrape(t, tel)
	const name = "fleet_agent_typing_refused_total"
	for reason, want := range map[string]string{"newline": "2", "pane": "1", "empty": "1"} {
		if got := series(body, name, `reason="`+reason+`"`); got != want {
			t.Errorf("%s{reason=%q} = %q, want %q\n%s", name, reason, got, want, body)
		}
	}
	if got := series(body, name, `reason="too_long"`); got != "" {
		t.Errorf("a reason never refused has a series: %s", got)
	}
	if strings.Contains(body, "MARKER") {
		t.Fatalf("the scrape holds the text or the pane:\n%s", body)
	}

	out := logs.String()
	if strings.Contains(out, "MARKER") {
		t.Fatalf("a log line holds the text or the pane:\n%s", out)
	}
	var refused []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "typing.refused" {
			refused = append(refused, m)
		}
	}
	if len(refused) != 4 {
		t.Fatalf("typing.refused lines = %d, want 4:\n%s", len(refused), out)
	}
	first := refused[0]
	if first["reason"] != "newline" || first["purpose"] != "operator_text" || first["level"] != "warn" {
		t.Fatalf("first typing.refused = %v", first)
	}
	for _, m := range refused {
		for k := range m {
			switch k {
			case "time", "level", "msg", "reason", "purpose", "service", "service.version", "service.instance.id", "machine_id", "component":
			default:
				t.Errorf("typing.refused has field %q; only reason and purpose are allowed", k)
			}
		}
	}
}

// A nil Recorder, and one made from a nil Telemetry, count and log nowhere
// and still refuse.
func TestNilRecorderStillRefuses(t *testing.T) {
	t.Parallel()
	rec, err := typing.NewRecorder(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*typing.Recorder{nil, rec} {
		if _, err := typing.NewLiteral(r, pk("%1"), typing.PurposeOperatorText, "a\nb"); err == nil {
			t.Fatal("a newline was accepted")
		}
	}
}

// Sent logs typing.sent with ids and counts only: the purpose, the server
// kind, the pane id and the bytes of text; never the text. The zero Line and
// a nil Recorder log nothing.
func TestSentLogsIDsAndCountsOnly(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-2", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	rec, err := typing.NewRecorder(tel)
	if err != nil {
		t.Fatal(err)
	}
	probe := &typev1.PaneKey{Server: "fleet-probe", ServerStartId: "p1", PaneId: "%7"}
	text, err := typing.NewLiteral(rec, probe, typing.PurposeProbeCommand, "MARKER /usage")
	if err != nil {
		t.Fatal(err)
	}
	enter, err := typing.NewKeys(rec, pk("%2"), typing.PurposeGoalStart, typing.KeyEnter)
	if err != nil {
		t.Fatal(err)
	}
	rec.Sent(t.Context(), text)
	rec.Sent(t.Context(), enter)
	rec.Sent(t.Context(), typing.Line{})
	(*typing.Recorder)(nil).Sent(t.Context(), text)

	out := logs.String()
	if strings.Contains(out, "MARKER") {
		t.Fatalf("typing.sent holds the text:\n%s", out)
	}
	var sent []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "typing.sent" {
			sent = append(sent, m)
		}
	}
	if len(sent) != 2 {
		t.Fatalf("typing.sent lines = %d, want 2:\n%s", len(sent), out)
	}
	want := []map[string]any{
		{"purpose": "probe_command", "server_kind": "probe", "pane_id": "%7", "bytes": float64(len("MARKER /usage")), "level": "info"},
		{"purpose": "goal_start", "server_kind": "default", "pane_id": "%2", "bytes": float64(0), "level": "info"},
	}
	for i, w := range want {
		for k, v := range w {
			if sent[i][k] != v {
				t.Errorf("typing.sent %d: %s = %v, want %v", i, k, sent[i][k], v)
			}
		}
	}
}
