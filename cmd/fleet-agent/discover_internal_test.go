package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// countingDiscover finds nothing and counts its browses.
type countingDiscover struct{ n atomic.Int64 }

func (d *countingDiscover) Browse(context.Context, time.Duration, func(conn.Service)) error {
	d.n.Add(1)
	return nil
}

// enroll with no --hub looks for the hub with the finder it was given, and
// when that finds nothing exits 1 naming --hub, with nothing sent.
func TestEnrollWithoutAHubUsesItsFinder(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	d := &countingDiscover{}
	dir := filepath.Join(t.TempDir(), "state")
	var out, errOut bytes.Buffer
	code := execute(context.Background(), []string{"--state-dir", dir, "--config-dir", filepath.Join(t.TempDir(), "config"), "enroll", "--token", h.Token(t, time.Now().Add(time.Hour))},
		&out, &errOut, d)
	if code != 1 || !strings.Contains(errOut.String(), "no hub found on the local network") ||
		!strings.Contains(errOut.String(), "--hub") {
		t.Errorf("enroll = %d %q, want 1 naming --hub", code, errOut.String())
	}
	if d.n.Load() != 1 || h.Requests() != 0 {
		t.Errorf("browses %d, hub requests %d; want 1 and 0", d.n.Load(), h.Requests())
	}
}

// run hands its finder to the connection loop: with the cached address
// refusing, the loop browses with it.
func TestRunBrowsesWithItsFinder(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	if err := os.WriteFile(filepath.Join(s.dir, conn.HubAddressFile), []byte(closed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &countingDiscover{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgent(ctx, s.dir, s.cfg, "test", "boot", s.tel, d) }()
	deadline := time.Now().Add(10 * time.Second)
	for d.n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("runAgent = %v", err)
	}
	if d.n.Load() == 0 {
		t.Errorf("run never browsed with its finder; log:\n%s", s.logs.String())
	}
}
