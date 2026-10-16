package system

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// TestBoundaries checks each threshold from both sides with hand-computed
// values: exactly on a level is still OK (levels are strictly past the
// threshold: over for ABOVE, under for BELOW), just under stays below the
// level, and just over (TestSignalLevels) reaches it. One row per level of
// every single-measure signal, including the log CRITICAL level.
func TestBoundaries(t *testing.T) {
	gib := float64(int64(1) << 30)
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	ok := hubv1.SystemState_SYSTEM_STATE_OK
	warn := hubv1.SystemState_SYSTEM_STATE_WARNING
	crit := hubv1.SystemState_SYSTEM_STATE_CRITICAL
	agent := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED
	usage := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS
	sync := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG
	outbox := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG
	drops := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS
	disk := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE
	clock := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW
	backup := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE
	log := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE
	cases := []struct {
		name  string
		kind  hubv1.SystemSignalKind
		value float64
		want  hubv1.SystemState
	}{
		{"agent warn exact", agent, 600, ok},
		{"agent warn under", agent, 599, ok},
		{"agent crit exact", agent, 3600, warn},
		{"agent crit under", agent, 3599, warn},
		{"usage warn exact", usage, 1800, ok},
		{"usage warn under", usage, 1799, ok},
		{"usage crit exact", usage, 7200, warn},
		{"usage crit under", usage, 7199, warn},
		{"sync warn exact", sync, 300, ok},
		{"sync warn under", sync, 299, ok},
		{"sync crit exact", sync, 600, warn},
		{"sync crit under", sync, 599, warn},
		{"outbox warn exact", outbox, 120, ok},
		{"outbox warn under", outbox, 119, ok},
		{"outbox crit exact", outbox, 600, warn},
		{"outbox crit under", outbox, 599, warn},
		{"drops warn exact", drops, 3, ok},
		{"drops warn under", drops, 2, ok},
		{"drops crit exact", drops, 20, warn},
		{"drops crit under", drops, 19, warn},
		{"disk warn exact", disk, 10 * gib, ok},
		{"disk warn under", disk, 10*gib - 1, warn},
		{"disk crit exact", disk, 2 * gib, warn},
		{"disk crit under", disk, 2*gib - 1, crit},
		{"disk crit over", disk, 2*gib + 1, warn},
		{"clock warn exact", clock, 300, ok},
		{"clock warn under", clock, 299, ok},
		{"clock warn exact neg", clock, -300, ok},
		{"clock crit exact", clock, 900, warn},
		{"clock crit under", clock, 899, warn},
		{"backup warn exact", backup, 36 * 3600, ok},
		{"backup warn under", backup, 36*3600 - 1, ok},
		{"backup crit exact", backup, 7 * 24 * 3600, warn},
		{"backup crit under", backup, 7*24*3600 - 1, warn},
		{"log warn exact", log, 100, ok},
		{"log warn under", log, 99, ok},
		{"log crit exact", log, 100, ok},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := drive(c.kind, start, 15*time.Second, 3, Input{Main: Known(c.value)})
			if r.State != c.want {
				t.Fatalf("value %v: state = %v, want %v", c.value, r.State, c.want)
			}
		})
	}
}

// TestWriterBoundaries checks the two writer child measures from both
// sides: commit time warns strictly over 250 ms (no CRITICAL level),
// queue-full is CRITICAL strictly over 30 s (no WARNING level).
func TestWriterBoundaries(t *testing.T) {
	kind := hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	step := 15 * time.Second
	ok := hubv1.SystemState_SYSTEM_STATE_OK
	warn := hubv1.SystemState_SYSTEM_STATE_WARNING
	crit := hubv1.SystemState_SYSTEM_STATE_CRITICAL
	commitKind := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_COMMIT_TIME
	queueKind := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_WRITER_QUEUE_FULL
	unspec := hubv1.SystemMeasureKind_SYSTEM_MEASURE_KIND_UNSPECIFIED
	cases := []struct {
		name  string
		in    Input
		want  hubv1.SystemState
		setBy hubv1.SystemMeasureKind
	}{
		{"commit exact", Input{Main: Known(0.1), CommitTime: Known(0.25)}, ok, unspec},
		{"commit under", Input{Main: Known(0.1), CommitTime: Known(0.24)}, ok, unspec},
		{"commit over", Input{Main: Known(0.1), CommitTime: Known(0.26)}, warn, commitKind},
		{"queue exact", Input{Main: Known(0.1), QueueFull: Known(30)}, ok, unspec},
		{"queue under", Input{Main: Known(0.1), QueueFull: Known(29)}, ok, unspec},
		{"queue over", Input{Main: Known(0.1), QueueFull: Known(31)}, crit, queueKind},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := drive(kind, start, step, 2, c.in)
			if r.State != c.want {
				t.Fatalf("state = %v, want %v", r.State, c.want)
			}
			if r.SetBy != c.setBy {
				t.Fatalf("set by = %v, want %v", r.SetBy, c.setBy)
			}
		})
	}
}
