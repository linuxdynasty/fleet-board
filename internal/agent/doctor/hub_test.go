package doctor_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func (r *rig) writeIdentityWithAddrs(certPEM string, notAfter time.Time, addrs []string) {
	r.t.Helper()
	data, err := json.Marshal(map[string]any{
		"version": 1, "machine_id": machineID, "machine_name": "box", "fleet_id": "f_1",
		"ca_fingerprint": "x", "cert_not_after": notAfter, "salt_version": 1, "account_salt": "AAAA",
		"cert_pem": certPEM, "ca_pem": r.caPEM, "key_pem": fakeKey, "enrolled_time": now,
		"hub_addresses": addrs,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(r.state, "identity.json", string(data), 0o600)
}

func hubOpts(r *rig, hs func(ctx context.Context, addr, caPEM string) error) doctor.Options {
	o := r.opts()
	o.HubHandshake = hs
	return o
}

func runHub(r *rig, hs func(ctx context.Context, addr, caPEM string) error) doctor.Result {
	t := r.t
	t.Helper()
	rep := doctor.Run(t.Context(), hubOpts(r, hs), doctor.Local()...)
	for _, c := range rep.Checks {
		if c.ID == "hub_handshake" {
			return c
		}
	}
	t.Fatal(`no check "hub_handshake"`)
	return doctor.Result{}
}

func TestHubHandshakeNeedsAnIdentity(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := runHub(r, func(ctx context.Context, addr, caPEM string) error { return nil })
	if got.Status != doctor.Unknown {
		t.Errorf("status = %s, want unknown", got.Status)
	}
}

func TestHubHandshakeNeedsAHandshakeFunc(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	rep := doctor.Run(t.Context(), r.opts(), doctor.Local()...)
	for _, c := range rep.Checks {
		if c.ID == "hub_handshake" {
			if c.Status != doctor.Unknown {
				t.Errorf("status = %s, want unknown without a handshake func", c.Status)
			}
			return
		}
	}
	t.Fatal(`no check "hub_handshake"`)
}

func TestHubHandshakeReachesTheHub(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	na := now.Add(90 * 24 * time.Hour)
	r.writeIdentityWithAddrs(r.leaf(machineID, now.Add(-time.Hour), na), na, []string{"hub.example:7443"})
	var sawAddr, sawCA string
	got := runHub(r, func(ctx context.Context, addr, caPEM string) error {
		sawAddr, sawCA = addr, caPEM
		return nil
	})
	if got.Status != doctor.OK {
		t.Fatalf("status = %s (%q), want ok", got.Status, got.Reason)
	}
	if sawAddr != "hub.example:7443" {
		t.Errorf("dialed %q, want hub.example:7443", sawAddr)
	}
	if sawCA == "" || sawCA == fakeKey {
		t.Error("handshake did not receive the CA PEM")
	}
	if strings.Contains(got.Reason, fakeKey) {
		t.Error("reason prints the key")
	}
}

func TestHubHandshakeWarnsWhenTheHubIsDown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	na := now.Add(90 * 24 * time.Hour)
	r.writeIdentityWithAddrs(r.leaf(machineID, now.Add(-time.Hour), na), na, []string{"hub.example:7443"})
	got := runHub(r, func(ctx context.Context, addr, caPEM string) error {
		return errors.New("connection refused")
	})
	if got.Status != doctor.Warn {
		t.Errorf("status = %s (%q), want warn", got.Status, got.Reason)
	}
}

func TestHubHandshakeFailsWhenTheHubIsUntrusted(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	na := now.Add(90 * 24 * time.Hour)
	r.writeIdentityWithAddrs(r.leaf(machineID, now.Add(-time.Hour), na), na, []string{"hub.example:7443"})
	got := runHub(r, func(ctx context.Context, addr, caPEM string) error {
		return doctor.ErrHubUntrusted
	})
	if got.Status != doctor.Fail {
		t.Errorf("status = %s (%q), want fail", got.Status, got.Reason)
	}
}

func TestHubHandshakeTriesTheCachedAddressFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	na := now.Add(90 * 24 * time.Hour)
	r.writeIdentityWithAddrs(r.leaf(machineID, now.Add(-time.Hour), na), na, []string{"configured.example:7443"})
	r.write(r.state, "hub-address", "cached.example:7443\n", 0o600)
	var tried []string
	got := runHub(r, func(ctx context.Context, addr, caPEM string) error {
		tried = append(tried, addr)
		return nil
	})
	if got.Status != doctor.OK {
		t.Fatalf("status = %s, want ok", got.Status)
	}
	if len(tried) == 0 || tried[0] != "cached.example:7443" {
		t.Errorf("tried = %v, want cached first", tried)
	}
}

func TestHubHandshakeWithNoAddressIsUnknown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	got := runHub(r, func(ctx context.Context, addr, caPEM string) error { return nil })
	if got.Status != doctor.Unknown {
		t.Errorf("status = %s (%q), want unknown with no address", got.Status, got.Reason)
	}
}
