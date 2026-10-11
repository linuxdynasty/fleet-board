package ring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openInternal(t *testing.T) *DB {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fleet-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// store.Write says a context error leaves the outcome unknown: the job may
// still commit. The unacked count follows the commit, so a caller that gave
// up never leaves it behind the file; a job that fails never moves it.
func TestWriteThenRunsAfterACommitTheCallerGaveUpOn(t *testing.T) {
	t.Parallel()
	db := openInternal(t)
	release := make(chan struct{})
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	ran, after := make(chan struct{}), make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- db.writeThen(ctx, func(*sql.Tx) error {
			close(started)
			<-release // the job holds the writer; the caller's context ends meanwhile
			close(ran)
			return nil
		}, func() { close(after) })
	}()
	<-started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("writeThen = %v, want the caller's context error", err)
	}
	select {
	case <-after:
		t.Fatal("after ran before the job committed")
	default:
	}
	close(release)
	eventually(t, "after runs once the job committed", func() bool {
		select {
		case <-after:
			return true
		default:
			return false
		}
	})
	<-ran

	// A failing job never runs after.
	bad := errors.New("job failed")
	failed := false
	if err := db.writeThen(context.Background(), func(*sql.Tx) error { return bad }, func() { failed = true }); !errors.Is(err, bad) {
		t.Fatalf("writeThen = %v, want the job's error", err)
	}
	if failed {
		t.Error("after ran for a job that failed")
	}
}
