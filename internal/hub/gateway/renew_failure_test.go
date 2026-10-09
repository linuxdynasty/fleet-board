package gateway_test

import (
	"crypto/rand"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// switchReader reads from crypto/rand until broken, then fails every read:
// the machine can enroll first, and only the renewal meets a dead source.
type switchReader struct{ broken atomic.Bool }

func (r *switchReader) Read(p []byte) (int, error) {
	if r.broken.Load() {
		return 0, errors.New("no randomness")
	}
	return rand.Read(p)
}

// The hub's own failures while renewing (a random source that fails when
// the certificate is signed, a stored certificate that cannot be read back)
// answer INTERNAL (02g), count as failed and log the step that broke.
// Neither leaves a half-done renewal.
func TestRenewCertAnswersInternalWhenTheHubFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// run breaks the hub and calls RenewCert, returning its error.
		run     func(t *testing.T, h *hub, m machine, src *switchReader) error
		step    string
		renewed int // renewals that succeeded before the failing call
	}{
		{"sign", func(t *testing.T, h *hub, m machine, src *switchReader) error {
			t.Helper()
			src.broken.Store(true)
			_, err := h.renew(t, newAgentKey(t).csr, m.cert)
			return err
		}, "sign", 0},
		{"encode", func(t *testing.T, h *hub, m machine, _ *switchReader) error {
			t.Helper()
			k := newAgentKey(t)
			h.mustRenew(t, k.csr, m.cert)
			h.exec(t, `UPDATE cert_renewals SET cert = x'00' WHERE machine_id = ?`, m.id)
			_, err := h.renew(t, k.csr, m.cert) // the replay reads the unreadable certificate
			return err
		}, "encode", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := &switchReader{}
			h := startHub(t, func(o *gateway.ServerOptions) { o.Random = src })
			m := h.enrollMachine(t)
			serials := h.serialRows(t)

			err := tc.run(t, h, m, src)

			want := refusal{code: connect.CodeInternal, reason: typev1.ReasonCode_REASON_CODE_INTERNAL}
			if got := refusalOf(t, err); got != want {
				t.Fatalf("refusal %+v (%v), want %+v", got, err, want)
			}
			body := scrape(t, h.fixture)
			if n := sample(body, "fleet_hub_agent_cert_renewals_total", `result="failed"`); n != 1 {
				t.Errorf("fleet_hub_agent_cert_renewals_total{result=failed} = %v, want 1", n)
			}
			if tc.renewed > 0 {
				if n := sample(body, "fleet_hub_agent_cert_renewals_total", `result="renewed"`); n != float64(tc.renewed) {
					t.Errorf("fleet_hub_agent_cert_renewals_total{result=renewed} = %v, want %d", n, tc.renewed)
				}
			}
			lines := h.events(t, "cert.renew_refused")
			if len(lines) != 1 || lines[0]["result"] != "failed" || lines[0]["step"] != tc.step ||
				lines[0]["machine_id"] != m.id {
				t.Errorf("cert.renew_refused lines %v, want one failed with step %s for %s", lines, tc.step, m.id)
			}
			if n := h.serialRows(t); n != serials+tc.renewed {
				t.Errorf("cert_serials went from %d to %d rows, want %d", serials, n, serials+tc.renewed)
			}
		})
	}
}
