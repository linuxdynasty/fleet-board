package conn_test

import (
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// A RenewCert that fails never ends the loop or drops the stream: an
// older hub's UNIMPLEMENTED, a busy hub's UNAVAILABLE, a refusal and the
// hub's own INTERNAL are each logged, counted and tried again at the next
// check with the same key, and the renewal goes through once the hub
// answers.
func TestAFailedRenewalNeverEndsTheLoop(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, event, result, code string
		err                       error
	}{
		{
			name: "unimplemented", event: "renew.failed", result: "failed", code: "unimplemented",
			err: connect.NewError(connect.CodeUnimplemented, errNoRenew),
		},
		{
			name: "unavailable", event: "renew.failed", result: "failed", code: "unavailable",
			err: connect.NewError(connect.CodeUnavailable, errNoRenew),
		},
		{
			name: "unavailable, retryable", event: "renew.failed", result: "failed", code: "unavailable",
			err: hubError(connect.CodeUnavailable, typev1.ReasonCode_REASON_CODE_UNSPECIFIED, true),
		},
		{
			name: "internal", event: "renew.failed", result: "failed", code: "internal",
			err: hubError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, false),
		},
		{
			name: "refused", event: "renew.refused", result: "refused", code: "unauthenticated",
			err: hubError(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, false),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			h.set(func(h *connectHub) { h.renew.fail = func(int) error { return tc.err } })
			a := enrolled(t, h)
			dueHub(h)
			r := startLoop(t, a, func(o *conn.LoopOptions) {
				o.RenewCheck = 10 * time.Millisecond
			})
			waitFor(t, 10*time.Second, "three tries", func() bool { return len(a.events(t, tc.event)) >= 3 })
			r.running(t)
			assertNothingSaved(t, a, h)
			got := a.events(t, tc.event)[0]
			if got["code"] != tc.code || (tc.event == "renew.failed" && got["step"] != "call") {
				t.Errorf("%s = %v, want code=%s", tc.event, got, tc.code)
			}
			if tc.result == "refused" && got["reason"] != "CREDENTIALS_INVALID" {
				t.Errorf("renew.refused = %v, want reason=CREDENTIALS_INVALID", got)
			}
			if m := metric(scrape(t, a), "fleet_agent_cert_renewals_total", `result="`+tc.result+`"`); m == "" || m == "0" {
				t.Errorf("fleet_agent_cert_renewals_total{result=%s} = %q", tc.result, m)
			}
			keys := h.renewKeys()
			for i, k := range keys {
				if !k.Equal(keys[0]) {
					t.Fatalf("try %d sent another CSR key; every try must send renew.key's", i+1)
				}
			}
			h.set(func(h *connectHub) { h.renew.fail = nil })
			waitFor(t, 10*time.Second, "the renewal once the hub answers", func() bool {
				return len(a.events(t, "renew.completed")) == 1
			})
			if renewed := savedPair(t, a); !keys[0].Equal(renewed.PublicKey) {
				t.Error("the saved certificate is not for the key every try sent")
			}
		})
	}
}

// An answer lost after the hub stored it (the stream reset) is a failed
// call; the next check sends the same CSR, the real hub returns the
// certificate it stored, and there is one renewal and one new serial.
func TestALostRenewalAnswerGetsTheStoredCertificate(t *testing.T) {
	t.Parallel()
	rh := startRenewHub(t, conntest.DropFirstReplyTo("RenewCert"))
	a := rh.enrollAt(t, overdue)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	r := startLoop(t, a, func(o *conn.LoopOptions) { o.RenewCheck = 10 * time.Millisecond })
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	r.running(t)
	if failed := a.events(t, "renew.failed"); len(failed) != 1 || failed[0]["step"] != "call" {
		t.Errorf("renew.failed = %v, want one failed call (the lost answer)", failed)
	}
	if n := len(rh.fake.calls.of("RenewCert")); n != 2 {
		t.Errorf("%d RenewCert calls, want 2 (the lost one and its retry)", n)
	}
	renewed := savedPair(t, a)
	if got := rh.Renewals(t, id.MachineID); len(got) != 1 || got[0] != serialOf(renewed) {
		t.Errorf("the hub stored renewals %v, want one, cert.pem's %s", got, serialOf(renewed))
	}
	if got := rh.Serials(t, id.MachineID); len(got) != 2 {
		t.Errorf("the hub stored serials %v, want the enrolled one and one renewal", got)
	}
	if exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
		t.Errorf("%s is left after the renewal", conn.RenewKeyFile)
	}
}
