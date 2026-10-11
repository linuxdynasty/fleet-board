package tmux_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// A text field over the bound is counted on every read in
// fleet_agent_tmux_inventory_fields_dropped_total{field, reason} and logged
// as tmux.inventory_field_dropped at most once a minute per field, with
// its byte count and how many lines were not logged: never the value.
func TestInventoryFieldDroppedMetric(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			srv.Run(t, "set-option", "-t", "work", "@fleet_owner", strings.Repeat("q", 2000))
			var clock atomic.Int64
			clock.Store(time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC).UnixNano())
			r := startClient(t, srv, nil, func(o *tmux.ClientOptions) {
				m, err := tmux.NewMetrics(o.Telemetry)
				if err != nil {
					t.Fatal(err)
				}
				tmux.SetClockForTest(m, func() time.Time { return time.Unix(0, clock.Load()) })
				o.Parser.Metrics = m
			})
			r.waitState(t, tmux.StateUp)
			read := func() {
				t.Helper()
				if _, err := r.ReadInventory(ctxT(t)); err != nil {
					t.Fatalf("ReadInventory: %v", err)
				}
			}
			read()
			read()
			clock.Add(int64(time.Minute))
			read()

			const name = "fleet_agent_tmux_inventory_fields_dropped_total"
			body := scrape(t, r.tel)
			if v := series(body, name, `field="fleet_owner"`, `reason="over_bound"`); v != "3" {
				t.Errorf("%s{field=fleet_owner,reason=over_bound} = %q, want 3", name, v)
			}
			var got []map[string]any
			for _, ev := range r.logs.events(t) {
				if ev["msg"] == "tmux.inventory_field_dropped" {
					got = append(got, ev)
				}
			}
			if len(got) != 2 {
				t.Fatalf("%d tmux.inventory_field_dropped lines, want 2 (one, then one a minute later):\n%s", len(got), r.logs.String())
			}
			for i, wantSuppressed := range []float64{0, 1} {
				e := got[i]
				if e["field"] != "fleet_owner" || e["reason"] != "over_bound" || e["bytes"] != float64(2000) || e["suppressed"] != wantSuppressed || e["level"] != "warn" {
					t.Errorf("line %d = %v, want field fleet_owner, reason over_bound, bytes 2000, suppressed %v", i, e, wantSuppressed)
				}
			}
			if strings.Contains(r.logs.String(), "qqqq") || strings.Contains(body, "qqqq") {
				t.Error("the value reached a log line or the scrape")
			}
		})
	}
}
