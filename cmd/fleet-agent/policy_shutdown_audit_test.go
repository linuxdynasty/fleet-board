package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

func TestPolicyShutdownAuditsAChangeWhenTheWatchCouldNotStart(t *testing.T) {
	t.Parallel()
	for _, setupErr := range []error{syscall.EMFILE, os.ErrNotExist} {
		t.Run(setupErr.Error(), func(t *testing.T) {
			t.Parallel()
			s := newStartup(t)
			if err := os.MkdirAll(s.cfg, 0o700); err != nil {
				t.Fatal(err)
			}
			al, err := audit.New(s.dir, audit.Options{Telemetry: s.tel})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = al.Close() })
			mgr, err := policy.Load(policy.Options{
				Dir: s.cfg, Telemetry: s.tel, Audit: al,
				NewWatcher: func() (policy.Watcher, error) { return nil, setupErr },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.Watch(context.Background()); err != nil || !mgr.WatchOff() {
				t.Fatalf("watch failure: off=%v err=%v", mgr.WatchOff(), err)
			}
			if err := os.WriteFile(filepath.Join(s.cfg, policy.File), []byte("schema_version: \"1\"\n"), 0o644); err != nil { //nolint:gosec // G306: policy files are 0644 by contract.
				t.Fatal(err)
			}
			p := &policyRun{mgr: mgr, audit: al, stopWatch: func() {}, closeManager: mgr.Close, closeAudit: func() {
				if err := al.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					t.Error(err)
				}
			}}
			p.close()
			rows := auditRows(t, s.dir)
			if len(rows) != 1 || rows[0]["action"] != "policy.changed" {
				t.Fatalf("audit rows after unavailable-watch shutdown = %v; want one policy.changed", rows)
			}
		})
	}
}
