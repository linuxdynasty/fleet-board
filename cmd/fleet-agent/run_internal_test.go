package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// startup is a runAgent harness: an enrolled state folder, its machine id
// and the log buffer runAgent writes to.
type startup struct {
	dir     string
	machine string
	tel     *telemetry.Telemetry
	logs    *conntest.Buffer
}

func newStartup(t *testing.T) *startup {
	t.Helper()
	h := conntest.StartHub(t, conntest.Options{})
	logs := &conntest.Buffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := conn.Enroll(context.Background(), conn.Options{
		Dir: dir, Hub: h.Addr, Token: h.Token(t, time.Now().Add(time.Hour)), Version: "test", Telemetry: tel,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := conn.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &startup{dir: dir, machine: id.MachineID, tel: tel, logs: logs}
}

// run calls runAgent with ctx, which the caller may have cancelled.
func (s *startup) run(ctx context.Context) error {
	return runAgent(ctx, s.dir, "test", "boot", s.tel)
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// A signal during startup is a clean stop: runAgent with its context
// already cancelled returns nil (exit 0) and logs exactly one run.stopped
// naming the step the cancellation reached (the store, the first step that
// takes the context) and the machine, and no run.failed.
func TestASignalDuringStartupIsACleanStop(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	if err := s.run(cancelled()); err != nil {
		t.Errorf("runAgent after a signal = %v, want nil (exit 0); log:\n%s", err, s.logs.String())
	}
	stopped := conntest.Lines(t, s.logs.String(), "run.stopped")
	if len(stopped) != 1 || stopped[0]["step"] != "store" || stopped[0]["machine_id"] != s.machine {
		t.Errorf("run.stopped lines = %v; want one {step=store, machine_id=%s}", stopped, s.machine)
	}
	if lines := conntest.Lines(t, s.logs.String(), "run.failed"); len(lines) != 0 {
		t.Errorf("a signal during startup logged run.failed: %v", lines)
	}
}

// A signal that arrives at the same time as a real startup failure must not
// hide it: only a failure the cancellation itself caused is a clean stop.
// The identity, lock and ended steps never take the context, so each of
// these is still a commandError and one run.failed with its step and kind.
func TestASignalDoesNotHideARealStartupFailure(t *testing.T) {
	t.Parallel()
	chmod := func(name string, mode os.FileMode) func(t *testing.T, s *startup) {
		return func(t *testing.T, s *startup) {
			t.Helper()
			if err := os.Chmod(filepath.Join(s.dir, name), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeEnded := func(body string, mode os.FileMode) func(t *testing.T, s *startup) {
		return func(t *testing.T, s *startup) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(s.dir, conn.EndedFile), []byte(body), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name, step, kind string
		setup            func(t *testing.T, s *startup)
	}{
		{"not enrolled", "identity", "constraint", func(t *testing.T, s *startup) {
			t.Helper()
			if err := os.Remove(filepath.Join(s.dir, conn.IdentityFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"loose identity", "identity", "constraint", chmod(conn.IdentityFile, 0o644)},
		{"another run holds the lock", "lock", "constraint", func(t *testing.T, s *startup) {
			t.Helper()
			lk, err := lock.Acquire(filepath.Join(s.dir, lockFile))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lk.Release() })
		}},
		{"damaged ended.json", "ended", "parse", writeEnded("{not json", 0o600)},
		{"loose ended.json", "ended", "constraint", writeEnded("{}", 0o644)},
		{"loose agent.db", "store", "constraint", func(t *testing.T, s *startup) {
			t.Helper()
			chmod("agent.db", 0o644)(t, s)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStartup(t)
			tc.setup(t, s)
			err := s.run(cancelled())
			var ce commandError
			if !errors.As(err, &ce) {
				t.Fatalf("runAgent = %v, want a commandError (exit 1); log:\n%s", err, s.logs.String())
			}
			failed := conntest.Lines(t, s.logs.String(), "run.failed")
			if len(failed) != 1 || failed[0]["step"] != tc.step || failed[0]["err_kind"] != tc.kind {
				t.Errorf("run.failed lines = %v; want one {step=%s, err_kind=%s}", failed, tc.step, tc.kind)
			}
			if lines := conntest.Lines(t, s.logs.String(), "run.stopped"); len(lines) != 0 {
				t.Errorf("a real failure logged run.stopped: %v", lines)
			}
		})
	}
}
