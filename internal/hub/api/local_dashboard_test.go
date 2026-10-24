package api_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// dashboardServer starts the real local API listener for GET-path tests:
// the dashboard on every other path, the API and metrics untouched.
func dashboardServer(t *testing.T) string {
	t.Helper()
	e := newFleetEnv(t, localUser("owner"), trusted(false))
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel, Now: func() time.Time { return fleetNow },
		StateDir: e.dir, LocalTrusted: true, HubInstanceID: testInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	quotas, err := api.NewQuotas(config.RateLimits{
		Read: 100, Write: 100, Unauthenticated: 100, Metrics: 100,
	}, e.tel, func() time.Time { return fleetNow })
	if err != nil {
		t.Fatal(err)
	}
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("unexpected listener address type %T", listener.Addr())
	}
	srv, err := api.NewLocalServer(e.tel, tcpAddr.Port, fleet, quotas)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	httpSrv := httptest.NewUnstartedServer(srv.Handler)
	httpSrv.Listener = listener
	httpSrv.Start()
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

func getPath(t *testing.T, base, path string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func TestLocalServerServesTheDashboard(t *testing.T) {
	t.Parallel()
	base := dashboardServer(t)
	cases := []struct {
		name string
		path string
	}{
		{name: "root", path: "/"},
		{name: "client route falls back to the entry page", path: "/usage"},
		{name: "nested client route falls back to the entry page", path: "/session/x/windows"},
	}
	var entry string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, header, body := getPath(t, base, tc.path)
			if code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", tc.path, code)
			}
			if ctype := header.Get("Content-Type"); !strings.HasPrefix(ctype, "text/html") {
				t.Fatalf("GET %s Content-Type = %q, want text/html", tc.path, ctype)
			}
			if cache := header.Get("Cache-Control"); cache != "no-cache" {
				t.Fatalf("GET %s Cache-Control = %q, want no-cache", tc.path, cache)
			}
			if body == "" {
				t.Fatalf("GET %s body is empty", tc.path)
			}
			if entry == "" {
				entry = body
			} else if body != entry {
				t.Fatalf("GET %s body differs from the entry page", tc.path)
			}
		})
	}
}

func TestLocalServerKeepsApiAndMetrics(t *testing.T) {
	t.Parallel()
	base := dashboardServer(t)
	if code, _, _ := getPath(t, base, "/metrics"); code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", code)
	}
	// A Connect path without the protocol header is refused by the API,
	// which proves the dashboard fallback did not swallow it.
	_, _, entry := getPath(t, base, "/")
	code, _, body := getPath(t, base, "/fleet.hub.v1.FleetService/ListMachines")
	if body == entry {
		t.Fatal("API path fell back to the dashboard entry page")
	}
	if code == http.StatusOK {
		t.Fatalf("API path without a protocol header answered %d", code)
	}
}
