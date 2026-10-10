package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// lockProbe tries fleetd.lock and records whether someone else held it.
type lockProbe struct {
	path string
	mu   sync.Mutex
	got  map[string]error
}

func (p *lockProbe) try(when string) {
	l, err := lock.Acquire(p.path)
	if err == nil {
		err = l.Release() // nobody held it
		if err == nil {
			err = errors.New("the lock was free")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got[when] = err
}

// Write sees each log line; restore.completed is logged after the rename
// that puts the new store in place.
func (p *lockProbe) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"msg":"restore.completed"`)) {
		p.try("after the swap")
	}
	return len(b), nil
}

// #92: restore holds fleetd.lock for the whole run, not only at the start.
// It is probed while the identity is rewritten (Options.Hostname runs then,
// for the --address certificate names) and after the swap (when
// restore.completed is logged); both must find it held.
func TestRestoreHoldsTheLockForTheWholeRun(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	res, err := daemon.Backup(context.Background(), h.options(), daemon.BackupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	probe := &lockProbe{path: filepath.Join(h.state, daemon.LockName), got: map[string]error{}}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: probe})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	opts := h.options()
	opts.Telemetry = tel
	opts.Hostname = func() (string, error) {
		probe.try("during the rewrite")
		return testHost, nil
	}
	if _, err := daemon.Restore(context.Background(), opts, daemon.RestoreRequest{Backup: res.Path, Address: "hub2.example"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	for _, when := range []string{"during the rewrite", "after the swap"} {
		if err, ok := probe.got[when]; !ok || !errors.Is(err, lock.ErrHeld) {
			t.Errorf("fleetd.lock %s: probed %v, err %v; want held (lock.ErrHeld)", when, ok, err)
		}
	}
}
