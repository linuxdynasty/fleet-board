package api_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const port = 7080

func newTelemetry(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel
}

func get(h http.Handler, path, host, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:50000"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// reason decodes the Connect error body of 02f ("permission_denied" with an
// ErrorDetail) and returns its reason code.
func reason(t *testing.T, body []byte) typev1.ReasonCode {
	t.Helper()
	var e struct {
		Code    string `json:"code"`
		Details []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("refusal body %q is not a Connect error: %v", body, err)
	}
	if e.Code != "permission_denied" || len(e.Details) != 1 || e.Details[0].Type != "fleet.type.v1.ErrorDetail" {
		t.Fatalf("refusal body %s, want permission_denied with one fleet.type.v1.ErrorDetail", body)
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(e.Details[0].Value, "="))
	if err != nil {
		t.Fatal(err)
	}
	var d typev1.ErrorDetail
	if err := proto.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d.GetReason()
}

func TestHostAndOriginChecksComeBeforeAnyHandler(t *testing.T) {
	t.Parallel()
	const (
		host   = typev1.ReasonCode_REASON_CODE_HOST_NOT_ALLOWED
		origin = typev1.ReasonCode_REASON_CODE_ORIGIN_NOT_ALLOWED
		none   = typev1.ReasonCode_REASON_CODE_UNSPECIFIED
	)
	cases := []struct {
		name, path, host, origin string
		status                   int
		reason                   typev1.ReasonCode
	}{
		{"127.0.0.1", "/metrics", "127.0.0.1:7080", "", 200, none},
		{"localhost", "/metrics", "localhost:7080", "", 200, none},
		{"::1", "/metrics", "[::1]:7080", "", 200, none},
		{"host is case-insensitive", "/metrics", "LocalHost:7080", "", 200, none},
		{"loopback origin", "/metrics", "127.0.0.1:7080", "http://127.0.0.1:7080", 200, none},
		{"localhost origin", "/metrics", "localhost:7080", "http://localhost:7080", 200, none},
		{"foreign host (DNS rebinding)", "/metrics", "evil.example:7080", "", 403, host},
		{"loopback name on another port", "/metrics", "127.0.0.1:7081", "", 403, host},
		{"no port", "/metrics", "localhost", "", 403, host},
		{"another loopback address", "/metrics", "127.0.0.2:7080", "", 403, host},
		{"foreign origin", "/metrics", "127.0.0.1:7080", "http://evil.example", 403, origin},
		{"null origin", "/metrics", "127.0.0.1:7080", "null", 403, origin},
		{"https origin", "/metrics", "127.0.0.1:7080", "https://127.0.0.1:7080", 403, origin},
		{"origin on another port", "/metrics", "127.0.0.1:7080", "http://127.0.0.1:7081", 403, origin},
		{"foreign host on an unknown path", "/nope", "evil.example", "", 403, host},
		{"healthz is not served", "/healthz", "127.0.0.1:7080", "", 404, none},
		{"root is not served", "/", "127.0.0.1:7080", "", 404, none},
		{"no FleetService", "/fleet.hub.v1.FleetService/GetServerInfo", "127.0.0.1:7080", "", 404, none},
	}
	tel := newTelemetry(t)
	h := api.Handler(tel, port)
	refusals := map[string]int{}
	for _, tc := range cases {
		rec := get(h, tc.path, tc.host, tc.origin)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, rec.Code, tc.status)
			continue
		}
		for k := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: response carries CORS header %s", tc.name, k)
			}
		}
		switch {
		case tc.status == 200 && !strings.Contains(rec.Body.String(), "go_goroutines"):
			t.Errorf("%s: 200 without the metrics text", tc.name)
		case tc.status == 403:
			if got := reason(t, rec.Body.Bytes()); got != tc.reason {
				t.Errorf("%s: reason %v, want %v", tc.name, got, tc.reason)
			}
			if tc.reason == host {
				refusals["host"]++
			} else {
				refusals["origin"]++
			}
		}
	}
	body := get(h, "/metrics", "127.0.0.1:7080", "").Body.String()
	for r, want := range refusals {
		if got := sample(body, "fleet_hub_auth_failures_total", `reason="`+r+`"`); got != float64(want) {
			t.Errorf("fleet_hub_auth_failures_total{reason=%q} = %v, want %d", r, got, want)
		}
	}
}

// The metrics handler keeps its own loopback-peer rule behind the guard.
func TestARemotePeerWithAGoodHostIsStillRefusedMetrics(t *testing.T) {
	t.Parallel()
	h := api.Handler(newTelemetry(t), port)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Host = "127.0.0.1:7080"
	req.RemoteAddr = "192.168.1.20:50000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

func sample(body, name, want string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, name+"{") && strings.Contains(line, want) {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}
