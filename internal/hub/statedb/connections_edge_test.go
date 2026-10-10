package statedb_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// 02g asks of Connect a live machine row and a serial that is not revoked,
// not one stored in cert_serials: a hub restored from a backup taken before
// the machine renewed its certificate has no row for the serial, and TLS
// has already verified the certificate.
func TestHelloAcceptsASerialTheStoreDoesNotHave(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `DELETE FROM cert_serials`); err != nil {
		t.Fatal(err)
	}
	out, err := statedb.Hello(context.Background(), s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if err != nil || out.Epoch != 1 {
		t.Fatalf("Hello with the serial row gone = %+v, %v; want epoch 1", out, err)
	}
	in := hello(1, "boot-b", "192.0.2.7:51001", helloAt.Add(time.Second))
	in.PeerSerial = "cd0042" // a renewed certificate the restored store never saw
	if out, err := statedb.Hello(context.Background(), s, in); err != nil || out.Epoch != 2 {
		t.Errorf("Hello with a serial never stored = %+v, %v; want epoch 2", out, err)
	}
}

// A hub clock that steps back never moves last_seen_time back and never
// stores an end before a start: the end is clamped to the start.
func TestTimesNeverStepBack(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	back := helloAt.Add(-time.Hour)
	mustHello(t, s, hello(0, "boot-b", "192.0.2.7:51001", back)) // displaces epoch 1 an hour before it started
	if live, err := statedb.EndConnection(context.Background(), s, renewMachineID, 2, statedb.EndStreamClosed, back.Add(-time.Hour)); err != nil || !live {
		t.Fatalf("EndConnection = %v, %v", live, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if r := readConn(t, db, renewMachineID, 1); r.ended.Int64 != r.started || r.started != helloAt.UnixMilli() {
		t.Errorf("displaced row started %d ended %d, want the end clamped to the start", r.started, r.ended.Int64)
	}
	if r := readConn(t, db, renewMachineID, 2); r.ended.Int64 != r.started || r.started != back.UnixMilli() {
		t.Errorf("closed row started %d ended %d, want the end clamped to the start", r.started, r.ended.Int64)
	}
	if _, _, seen := machineMarks(t, db, renewMachineID); seen.Int64 != helloAt.UnixMilli() {
		t.Errorf("last_seen_time %v, want it kept at %d", seen, helloAt.UnixMilli())
	}
}

// Hello takes the caller's LiveStream by value: the registry copies it
// under its own lock, and its reader may keep stamping heartbeats while the
// writer goroutine runs the job (run with -race).
func TestHelloCopiesTheLiveStreamItIsGiven(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	var mu sync.Mutex
	entry := &statedb.LiveStream{Epoch: 1, LastHeartbeat: helloAt}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { // the registry's reader
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			mu.Lock()
			entry.LastHeartbeat = entry.LastHeartbeat.Add(time.Millisecond)
			mu.Unlock()
		}
	}()
	in := hello(1, "boot-b", "192.0.2.7:51001", helloAt.Add(time.Hour))
	mu.Lock()
	in.Live = *entry
	mu.Unlock()
	out, err := statedb.Hello(context.Background(), s, in)
	close(stop)
	<-done
	if err != nil || out.Displaced == nil {
		t.Fatalf("Hello = %+v, %v", out, err)
	}
	if want := in.Now.Sub(in.Live.LastHeartbeat).Truncate(time.Millisecond); out.Displaced.HeartbeatAge != want || !out.Displaced.HeartbeatKnown {
		t.Errorf("heartbeat age %v, want %v from the copy taken", out.Displaced.HeartbeatAge, want)
	}
}
