package main_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// countingHub is a TCP listener that counts the connections made to it and
// closes each at once.
func countingHub(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	return ln.Addr().String(), &n
}

// enrolledDir enrolls a fresh state folder with h and returns it with the
// machine id.
func enrolledDir(t *testing.T, h *conntest.Hub) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(time.Hour))); r.code != 0 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	id, err := conn.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, id.MachineID
}

// writeEnded writes ended.json as the connection loop does, mode 0600.
func writeEnded(t *testing.T, dir, end, machineID string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"version": 1, "end": end, "machine_id": machineID, "peer": "hub.example:7443", "time": time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ended.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// After the hub revoked the machine, did not know it, or was another fleet,
// a restarted run (a supervisor) refuses before any network call, exit 1,
// naming the end and the fix. Removing ended.json lets it try again.
func TestARunAfterAnEndSendsNothing(t *testing.T) {
	t.Parallel()
	cases := []struct{ end, fix string }{
		{"revoked", "remove `identity.json` (keep `agent.db`), then `fleet-agent enroll --token <new>`"},
		{"unknown_machine", "remove `identity.json` (keep `agent.db`), then `fleet-agent enroll --token <new>`"},
		{"fleet_mismatch", "check the hub address, then re-enroll or remove `ended.json` to try again"},
	}
	for _, tc := range cases {
		t.Run(tc.end, func(t *testing.T) {
			t.Parallel()
			// Its own hub: another subtest's enroll must not show up in
			// this one's request count.
			h := conntest.StartHub(t, conntest.Options{})
			dir, machine := enrolledDir(t, h)
			// The cached address is tried first: point it at a counter.
			addr, dialed := countingHub(t)
			if err := os.WriteFile(filepath.Join(dir, "hub-address"), []byte(addr+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			writeEnded(t, dir, tc.end, machine)
			before := h.Requests()
			r := startAgent(t, "--state-dir", dir, "run")
			select {
			case <-r.done:
			case <-time.After(10 * time.Second):
				t.Fatalf("run after %s is still running: %s", tc.end, r.stderr.String())
			}
			out := r.stderr.String()
			refused := conntest.Lines(t, out, "run.refused")
			if r.code != 1 || len(refused) != 1 || refused[0]["end"] != tc.end || refused[0]["machine_id"] != machine ||
				!strings.Contains(out, tc.fix) {
				t.Errorf("run = %d %q; want 1, run.refused{end=%s, machine_id} and the fix %q", r.code, out, tc.end, tc.fix)
			}
			// The check comes before agent.db is opened, not only before a dial.
			if lines := conntest.Lines(t, out, "store.opened"); len(lines) != 0 {
				t.Errorf("a refused run opened agent.db first: %v", lines)
			}
			if n := dialed.Load(); n != 0 || h.Requests() != before {
				t.Errorf("run after %s made %d connections and %d hub requests, want none", tc.end, n, h.Requests()-before)
			}
			if _, err := os.Stat(filepath.Join(dir, "ended.json")); err != nil {
				t.Errorf("ended.json after a refused run: %v", err)
			}

			// The owner removes it to try again: the next run dials.
			if err := os.Remove(filepath.Join(dir, "ended.json")); err != nil {
				t.Fatal(err)
			}
			again := startAgent(t, "--state-dir", dir, "run")
			poll(t, 10*time.Second, "the run to dial the cached address", func() bool { return dialed.Load() > 0 })
			if again.exited() {
				t.Errorf("run without ended.json ended: %d %s", again.code, again.stderr.String())
			}
		})
	}
}

// ended.json for another machine id is a leftover of an earlier enrollment:
// run removes it, says so, and connects.
func TestARunRemovesAnEndForAnotherMachine(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, _ := enrolledDir(t, h)
	writeEnded(t, dir, "revoked", "01J0000000000000000000OTHER")
	before := h.Requests()
	r := startAgent(t, "--state-dir", dir, "run")
	poll(t, 10*time.Second, "the run to reach the hub", func() bool { return h.Requests() > before })
	if _, err := os.Stat(filepath.Join(dir, "ended.json")); !os.IsNotExist(err) {
		t.Errorf("ended.json for another machine: %v, want it removed", err)
	}
	if n := len(conntest.Lines(t, r.stderr.String(), "run.ended_removed")); n != 1 {
		t.Errorf("run.ended_removed lines = %d, want 1: %s", n, r.stderr.String())
	}
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-r.done
	if r.code != 0 {
		t.Errorf("run after SIGTERM = %d: %s", r.code, r.stderr.String())
	}
}
