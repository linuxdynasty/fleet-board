package machine_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// AgentHealth.rss_bytes is the agent's own resident memory, read on the 30 s
// sampler tick into the sample the heartbeat already holds.
func TestASampleCarriesTheAgentsOwnRSS(t *testing.T) {
	t.Parallel()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good()})
	if got := s.Latest().RSSBytes; got != 96<<20 {
		t.Fatalf("RSSBytes = %d, want 96 MiB", got)
	}
}

// A reading that fails is unknown, never 0 (principle 3). process_darwin.go
// ignores the return of proc_pidinfo, so gopsutil can report an RSS of 0 with
// no error: that too is a failed reading, counted as parse.
func TestARSSThatFailsOrIsZeroIsUnknownAndCounted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		rss     uint64
		err     error
		errKind string
	}{
		{"zero with no error", 0, nil, "parse"},
		{"error", 0, errors.New("read /proc/4242/statm of /home/secret-user"), "io"},
		{"error with a value", 5, errors.New("half read"), "io"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rd := good()
			rd.RSS = func(context.Context) (uint64, error) { return c.rss, c.err }
			tel, m, logs := newTel(t)
			smp := one(t, machine.SamplerOptions{StateDir: "/state", Readers: rd, Metrics: m}).Latest()
			if smp.RSSBytes != 0 {
				t.Errorf("RSSBytes = %d, want unknown (0)", smp.RSSBytes)
			}
			if smp.Resources.Load1 == nil || smp.Resources.MemTotalBytes == nil {
				t.Errorf("a failed RSS took the other readings with it: %v", smp.Resources)
			}
			ev := logs.events("sample.failed")
			if len(ev) != 1 || ev[0]["source"] != "rss" || ev[0]["err_kind"] != c.errKind || ev[0]["level"] != "warn" {
				t.Fatalf("want one warn sample.failed rss/%s, got %v", c.errKind, ev)
			}
			if v := seriesValue(scrape(t, tel), "fleet_agent_sample_failures_total{", `source="rss"`); v != "1" {
				t.Errorf("rss failures counted = %q, want 1", v)
			}
			for _, l := range logs.lines() {
				for k, v := range l {
					if str, ok := v.(string); ok && strings.Contains(str, "secret-user") {
						t.Errorf("log field %s leaked error text: %q", k, str)
					}
				}
			}
		})
	}
}

// RSS is read like the other readings: a read that never returns neither
// stalls the sample nor is started again while it is out.
func TestAHungRSSReadDoesNotStopTheSample(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int64
	rd := good()
	rd.RSS = func(context.Context) (uint64, error) { calls.Add(1); <-release; return 1, nil }
	_, m, logs := newTel(t)
	first, second := runTwo(t, machine.SamplerOptions{StateDir: "/state", Readers: rd, Metrics: m, ReadTimeout: 200 * time.Millisecond})
	for i, smp := range []*machine.Sample{first, second} {
		if smp.RSSBytes != 0 || smp.Resources.Load1 == nil {
			t.Fatalf("sample %d: RSSBytes = %d, load1 = %v; want RSS unknown and load kept", i+1, smp.RSSBytes, smp.Resources.Load1)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("the stuck RSS was read %d times, want 1", calls.Load())
	}
	if ev := logs.events("sample.failed"); len(ev) != 1 || ev[0]["source"] != "rss" || ev[0]["err_kind"] != "timeout" {
		t.Fatalf("want one sample.failed rss/timeout, got %v", ev)
	}
}
