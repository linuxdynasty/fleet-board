package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

func metricsAt(t *testing.T, h http.Handler, port int, peer string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	r.Host = fmt.Sprintf("127.0.0.1:%d", port)
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMetricsQuotaIsSharedByListenerAndPeerIP(t *testing.T) {
	tel := newTelemetry(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	q, err := api.NewQuotas(config.RateLimits{Read: 2, Write: 2, Unauthenticated: 2, Metrics: 1}, tel,
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, err := api.NewMetricsServer(tel, 7080, q)
	if err != nil {
		t.Fatal(err)
	}
	second, err := api.NewMetricsServer(tel, 7081, q)
	if err != nil {
		t.Fatal(err)
	}
	if got := metricsAt(t, first.Handler, 7080, "127.0.0.1:1000").Code; got != http.StatusOK {
		t.Fatalf("first request status %d, want 200", got)
	}
	refused := metricsAt(t, second.Handler, 7081, "127.0.0.1:2000")
	if refused.Code != http.StatusTooManyRequests || !strings.Contains(refused.Body.String(), "resource_exhausted") {
		t.Errorf("new port and listener returned %d %q, want RATE_LIMITED", refused.Code, refused.Body.String())
	}
	if got := metricsAt(t, second.Handler, 7081, "127.0.0.2:2000").Code; got != http.StatusOK {
		t.Errorf("distinct peer status %d, want 200", got)
	}
}

func TestMetricsPeerTableKeepsSpentAllowancesAtCapacity(t *testing.T) {
	tel := newTelemetry(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	q, err := api.NewQuotas(config.RateLimits{Read: 1, Write: 1, Unauthenticated: 1, Metrics: 1}, tel,
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.NewMetricsServer(tel, 7080, q)
	if err != nil {
		t.Fatal(err)
	}
	peer := func(i int) string {
		ip := netip.AddrFrom4([4]byte{127, byte(i >> 16), byte(i >> 8), byte(i)})
		return netip.AddrPortFrom(ip, 1000).String()
	}
	for i := range 1024 {
		if got := metricsAt(t, srv.Handler, 7080, peer(i)).Code; got != http.StatusOK {
			t.Fatalf("peer %d status %d, want 200", i, got)
		}
	}
	for _, i := range []int{0, 511, 1023, 1024, 1025} {
		if got := metricsAt(t, srv.Handler, 7080, peer(i)).Code; got != http.StatusTooManyRequests {
			t.Errorf("spent or new peer %d status %d, want 429", i, got)
		}
	}
	now = now.Add(time.Minute + time.Millisecond)
	if got := metricsAt(t, srv.Handler, 7080, peer(1024)).Code; got != http.StatusOK {
		t.Errorf("new peer after old debt expires status %d, want 200", got)
	}
	if got := metricsAt(t, srv.Handler, 7080, peer(0)).Code; got != http.StatusOK {
		t.Errorf("old peer after debt expires status %d, want 200", got)
	}
}
