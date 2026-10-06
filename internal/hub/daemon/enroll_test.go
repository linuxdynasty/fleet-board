package daemon_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// snapshot is every file under dir with its SHA-256 (a socket by its mode).
func snapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Type()&fs.ModeSocket != 0 {
			out[path] = sha256.Sum256([]byte(d.Type().String()))
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEnrollTokenAsksTheRunningHub(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	h.start(t, h.options())
	before := time.Now()
	resp, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{
		Machine: "laptop", ExpectHost: "laptop.lan", Labels: []string{"gpu"}, StartDrained: true,
	})
	if err != nil {
		t.Fatalf("EnrollToken: %v", err)
	}
	if !strings.HasPrefix(resp.GetToken(), "fbe1_") {
		t.Errorf("token %q", resp.GetToken())
	}
	left := resp.GetExpireTime().AsTime().Sub(before)
	if left < 15*time.Minute-time.Second || left > 15*time.Minute+5*time.Second {
		t.Errorf("expires in %v, want 15 min", left)
	}
	second, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{Machine: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if second.GetToken() == resp.GetToken() {
		t.Error("two calls printed the same token")
	}
}

func TestEnrollTokenLifetimeRefusals(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	h.start(t, h.options())
	for _, ttl := range []time.Duration{25 * time.Hour, 0} {
		_, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{Machine: "laptop", TTL: &ttl})
		if err == nil || !strings.Contains(err.Error(), "ttl") || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
			t.Errorf("ttl %v: %v, want a refusal naming ttl and INVALID_ARGUMENT", ttl, err)
		}
	}
	ttl := time.Hour
	resp, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{Machine: "laptop", TTL: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(resp.GetExpireTime().AsTime()); left < 59*time.Minute || left > time.Hour {
		t.Errorf("--ttl 1h expires in %v", left)
	}
}

// A hub that closes the connection unserved (the peer check) is not a
// refusal with a reason; the message says who may use the socket.
func TestEnrollTokenFromAnotherUserSaysWhoMayUseTheSocket(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	opts.PeerUID = func(*net.UnixConn) (uint32, error) { return uint32(os.Getuid()) + 1, nil }
	h.start(t, opts)
	_, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{Machine: "laptop"})
	if err == nil || !strings.Contains(err.Error(), "only the OS user that runs fleetd") ||
		strings.Contains(err.Error(), "the hub refused") {
		t.Errorf("EnrollToken: %v, want a message naming who may use admin.sock", err)
	}
}

func TestEnrollTokenWithNoHubSaysSoAndWritesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, h *hub)
	}{
		{"no socket", func(*testing.T, *hub) {}},
		{"a socket left by a killed hub", func(t *testing.T, h *hub) {
			ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.AdminSocketPath(h.state), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			ln.SetUnlinkOnClose(false)
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHub(t, "")
			h.init(t)
			tc.setup(t, h)
			before := snapshot(t, h.root)
			_, err := daemon.EnrollToken(context.Background(), h.options(), daemon.EnrollRequest{Machine: "laptop"})
			if !errors.Is(err, daemon.ErrNotRunning) || err.Error() != "fleetd is not running" {
				t.Errorf("EnrollToken: %v, want %q", err, daemon.ErrNotRunning)
			}
			if after := snapshot(t, h.root); !reflect.DeepEqual(before, after) {
				t.Errorf("files changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}
