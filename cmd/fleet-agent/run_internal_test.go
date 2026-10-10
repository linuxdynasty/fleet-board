package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// A signal during startup is a clean stop: runAgent with its context
// already cancelled returns nil (exit 0), whatever step it reached.
func TestASignalDuringStartupIsACleanStop(t *testing.T) {
	t.Parallel()
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runAgent(ctx, dir, "test", "boot", tel); err != nil {
		t.Errorf("runAgent after a signal = %v, want nil (exit 0); log:\n%s", err, logs.String())
	}
	if bytes.Contains([]byte(logs.String()), []byte(`"run.failed"`)) {
		t.Errorf("a signal during startup logged run.failed:\n%s", logs.String())
	}
}
