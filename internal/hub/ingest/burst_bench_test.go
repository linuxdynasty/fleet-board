package ingest_test

import (
	"bufio"
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// The row 5 ruling's fork 6 gate (04c): per-message Event jobs through the
// real store writer on a real state.db (every migration, synchronous=FULL,
// fullfsync), the S1 burst shape on one machine stream: 2,500 events/s for
// 60 s, 300-byte events, each one job of statedb.ApplyEvent (the history
// row, hub_last_event_seq and the hub_seq UPDATE), submitted in arrival
// order without waiting for the commit, as the gateway's Event arm does.
// One iteration is one whole run, so run it with -benchtime=1x:
//
//	FLEET_EVENT_BURST=1 CGO_ENABLED=0 go test -run '^$' -bench EventBurst -benchtime=1x -count=10 ./internal/hub/ingest/
//
// jobs/s is the jobs committed over the offered window; drain_ms is how
// long the last ones took to commit after the producer stopped (a writer
// that keeps up drains in about one enqueue-to-commit time).
// FLEET_EVENT_BURST_DURATION shortens the run for a smoke check. Budget:
// jobs/s >= 2,500 sustained, commit p99 <= 50 ms, enqueue to commit p99 <=
// 60 ms, writer busy <= 70 %.
const burstEnv = "FLEET_EVENT_BURST"

const (
	burstRate   = 2500
	burstBytes  = 300
	burstTick   = 10 * time.Millisecond
	burstMachID = "01jbenchmachine00000000000"
)

func BenchmarkEventBurst(b *testing.B) {
	if testing.Short() || os.Getenv(burstEnv) != "1" {
		b.Skipf("wall-clock measurement; set %s=1 and run without -short", burstEnv)
	}
	dur := 60 * time.Second
	if v, err := time.ParseDuration(os.Getenv(burstEnv + "_DURATION")); err == nil {
		dur = v
	}
	for b.Loop() {
		r := runBurst(b, dur)
		b.ReportMetric(r.jobsPerS, "jobs/s")
		b.ReportMetric(r.commitP99*1000, "commit_p99_ms")
		b.ReportMetric(r.waitP99*1000, "enqueue_commit_p99_ms")
		b.ReportMetric(r.waitP50*1000, "enqueue_commit_p50_ms")
		b.ReportMetric(100*r.busy, "busy_pct")
		b.ReportMetric(r.jobsPerBatch, "jobs/batch")
		b.ReportMetric(float64(r.failed), "failed")
		b.ReportMetric(r.drain*1000, "drain_ms")
	}
}

type burstResult struct {
	jobsPerS, commitP99, waitP50, waitP99, busy, jobsPerBatch, drain float64
	failed                                                           int
}

func runBurst(b *testing.B, dur time.Duration) burstResult {
	b.Helper()
	ctx := context.Background()
	tel, err := telemetry.New(telemetry.Config{
		Service: telemetry.Hub, Version: "bench", InstanceID: "bench",
		LogOutput: discard{},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tel.Shutdown(ctx) }()
	st, err := openState(ctx, b, tel)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seedMachine(b, st)
	s := ingest.Stream{MachineID: burstMachID, Epoch: 1}
	ev := burstEvent(b)
	before := scrape(b, tel)

	type pending struct {
		at time.Time
		p  *ingest.EventPending
	}
	queue := make(chan pending, 4*burstRate*int(dur/time.Second+1))
	var waits []time.Duration
	failed := 0
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for q := range queue { // outcomes come in submission order
			if err := <-q.p.Result(); err != nil {
				failed++
			}
			waits = append(waits, time.Since(q.at))
		}
	}()
	seq, prev := uint64(1000), uint64(0)
	// Offer exactly burstRate per second: each tick submits what is due
	// since start, so a late tick never lowers the offered rate.
	submitted := 0
	tick := time.NewTicker(burstTick)
	start := time.Now()
	for now := range tick.C {
		el := now.Sub(start)
		if el >= dur {
			break
		}
		for due := int(el.Seconds() * burstRate); submitted < due; submitted++ {
			seq++
			ev.Seq, ev.ObservedTime = seq, timestamppb.New(now)
			at := time.Now()
			in, err := ingest.EventIn(s, ev, prev, at)
			if err != nil {
				b.Fatal(err)
			}
			p, err := ingest.SubmitEvent(ctx, st, in, ev, nil)
			if err != nil {
				b.Fatal(err)
			}
			prev = seq
			queue <- pending{at: at, p: p}
		}
	}
	tick.Stop()
	offered := time.Since(start)
	close(queue)
	<-collected
	elapsed := time.Since(start)
	after := scrape(b, tel)

	delta := func(name string) float64 { return value(after, name) - value(before, name) }
	batches := delta("fleet_hub_writer_batch_seconds_count")
	slices.Sort(waits)
	return burstResult{
		jobsPerS:     float64(len(waits)-failed) / offered.Seconds(),
		drain:        (elapsed - offered).Seconds(),
		commitP99:    quantile(0.99, before, after, "fleet_hub_writer_batch_seconds"),
		waitP50:      waits[len(waits)/2].Seconds(),
		waitP99:      waits[int(0.99*float64(len(waits)-1))].Seconds(),
		busy:         delta("fleet_hub_writer_batch_seconds_sum") / elapsed.Seconds(),
		jobsPerBatch: float64(len(waits)) / math.Max(batches, 1),
		failed:       failed,
	}
}

// openState creates and opens a real state.db (every migration, the
// production pragmas) in a fresh 0700 folder.
func openState(ctx context.Context, b *testing.B, tel *telemetry.Telemetry) (*store.Store, error) {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		return nil, err
	}
	id := statedb.Identity{
		FleetID: "6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f60", CACert: []byte("ca"), CAKey: []byte("key"),
		ServerCert: []byte("cert"), ServerKey: []byte("key"), ServerNames: []string{"localhost"},
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "0b9d3c1e-6a2f-4e8d-8c7b-5a4f3e2d1c0b",
		CreatedTime: time.Now(),
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{Telemetry: tel}); err != nil {
		return nil, err
	}
	return statedb.Open(ctx, dir, tel)
}

// seedMachine makes one machine with a live connection on epoch 1 whose
// Snapshot is applied, through the real Hello and Snapshot jobs.
func seedMachine(b *testing.B, st *store.Store) {
	b.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, drained, install_id, state,
			hostname_mismatch, created_time) VALUES (?, 'bench', 'bench', 0, 'install-1', 'ENROLLED', 0, ?)`,
			burstMachID, now.UnixMilli())
		return err
	})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := statedb.Hello(ctx, st, statedb.HelloIn{
		MachineID: burstMachID, PeerSerial: "be0001", BootID: "boot-1", InstallID: "install-1", Peer: "192.0.2.1:5000",
		Reason: statedb.ConnectStart, Now: now,
	}); err != nil {
		b.Fatal(err)
	}
	p, err := ingest.SubmitSnapshot(ctx, st, ingest.Stream{MachineID: burstMachID, Epoch: 1, InstallID: "install-1"},
		&agentv1.Snapshot{Seq: 1000}, nil, now)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := p.Wait(ctx); err != nil {
		b.Fatal(err)
	}
}

// burstEvent is a replayed NEVER_DROP event whose wire size is burstBytes.
func burstEvent(b *testing.B) *agentv1.Event {
	b.Helper()
	ev := &agentv1.Event{
		Seq: 1_000_000, ObservedTime: timestamppb.Now(), Replayed: true,
		Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP,
		Body:     &agentv1.Event_HarnessCondition{HarnessCondition: &agentv1.HarnessCondition{}},
	}
	for n := 0; proto.Size(ev) < burstBytes; n++ {
		ev.GetHarnessCondition().Text = strings.Repeat("x", n)
	}
	if got := proto.Size(ev); got != burstBytes {
		b.Fatalf("event is %d bytes, want %d", got, burstBytes)
	}
	return ev
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func scrape(b *testing.B, tel *telemetry.Telemetry) string {
	b.Helper()
	req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Fatalf("scrape status %d", rec.Code)
	}
	return rec.Body.String()
}

// value is the sum of every series of name for db="state".
func value(body, name string) float64 {
	sum := 0.0
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if (strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ")) && strings.Contains(line, `db="state"`) {
			if v, err := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64); err == nil {
				sum += v
			}
		}
	}
	return sum
}

// quantile estimates a quantile of the batches added between two scrapes
// from the histogram buckets, interpolating inside a bucket as Prometheus
// does. The 50 ms budget is a bucket bound, so "p99 <= 50 ms" is exact.
func quantile(q float64, before, after, name string) float64 {
	type bucket struct{ le, count float64 }
	var bs []bucket
	sc := bufio.NewScanner(strings.NewReader(after))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"_bucket{") || !strings.Contains(line, `db="state"`) {
			continue
		}
		i := strings.Index(line, `le="`) + 4
		j := strings.Index(line[i:], `"`)
		le, err := strconv.ParseFloat(line[i:i+j], 64)
		if err != nil {
			le = math.Inf(1)
		}
		label := `le="` + line[i:i+j] + `"`
		c := bucketValue(after, name, label) - bucketValue(before, name, label)
		bs = append(bs, bucket{le, c})
	}
	if len(bs) == 0 {
		return math.NaN()
	}
	total := bs[len(bs)-1].count
	rank, lower, prev := q*total, 0.0, 0.0
	for _, bk := range bs {
		if bk.count >= rank {
			if math.IsInf(bk.le, 1) {
				return lower
			}
			return lower + (bk.le-lower)*(rank-prev)/math.Max(bk.count-prev, 1)
		}
		lower, prev = bk.le, bk.count
	}
	return lower
}

func bucketValue(body, name, label string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, name+"_bucket{") && strings.Contains(line, `db="state"`) && strings.Contains(line, label) {
			if v, err := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64); err == nil {
				return v
			}
		}
	}
	return 0
}
