package gateway_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// A store that cannot answer is the hub's failure: UNAVAILABLE with
// retryable set, counted failed, logged with step store.
func TestEnrollAnswersUnavailableWhenTheStoreFails(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := h.enroll(t, req)
	got := refusalOf(t, err)
	if got.code != connect.CodeUnavailable || !got.retryable {
		t.Fatalf("refusal %v retryable %v, want unavailable and retryable", got.code, got.retryable)
	}
	if n := sample(scrape(t, h.fixture), "fleet_hub_enroll_total", `result="failed"`); n != 1 {
		t.Errorf("fleet_hub_enroll_total{result=failed} = %v, want 1", n)
	}
	lines := h.events(t, "enroll.failed")
	if len(lines) != 1 || lines[0]["step"] != "store" || lines[0]["err_kind"] != "io" {
		t.Errorf("enroll.failed lines %v, want one with step store, err_kind io", lines)
	}
}

// Re-enroll is plan row 13a: a token naming a previous machine is never
// burned here, and the hub answers its own failure (step previous_machine).
func TestEnrollDoesNotBurnATokenNamingAPreviousMachine(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	tok := h.token(t, h.clock.now().Add(time.Hour))
	parsed, err := ca.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	// CreateEnrollmentToken refuses previous_machine until row 13a, so set it
	// the way a later row's token would carry it.
	err = h.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE enroll_tokens SET previous_machine = '01jold0000000000000000000a' WHERE secret_hash = ?`,
			parsed.SecretHash[:])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.enroll(t, enrollRequest(tok, newAgentKey(t), "install-1"))
	got := refusalOf(t, err)
	if got.code != connect.CodeInternal || got.reason != typev1.ReasonCode_REASON_CODE_INTERNAL || got.retryable {
		t.Fatalf("refusal %v / %v / retryable %v, want internal / INTERNAL / false", got.code, got.reason, got.retryable)
	}
	lines := h.events(t, "enroll.failed")
	if len(lines) != 1 || lines[0]["step"] != "previous_machine" || lines[0]["err_kind"] != "refused" {
		t.Errorf("enroll.failed lines %v, want one with step previous_machine, err_kind refused", lines)
	}
	var used sql.NullInt64
	err = h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT used_time FROM enroll_tokens WHERE secret_hash = ?`,
			parsed.SecretHash[:]).Scan(&used)
	})
	if err != nil {
		t.Fatal(err)
	}
	if used.Valid || h.machineCount(t) != 0 {
		t.Error("the token was burned or a machine was written")
	}
}

// Enroll ignores a client certificate on the connection: an agent that
// still holds an old certificate from this CA enrolls the same way.
func TestEnrollIgnoresAPresentedClientCertificate(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
	res, err := h.client(t, clientCert(t, h.auth)).Enroll(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("Enroll with a client certificate: %v", err)
	}
	if res.Msg.GetMachineId().GetValue() == "m-1" {
		t.Error("the machine id came from the presented certificate")
	}
}

// A request over the 02e message cap (1 MiB) is refused before the handler.
func TestEnrollRefusesAMessageOverTheCap(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	req := enrollRequest(strings.Repeat("a", 1<<20), newAgentKey(t), "install-1")
	_, err := h.enroll(t, req)
	if code := connect.CodeOf(err); code != connect.CodeResourceExhausted {
		t.Errorf("code %v (%v), want resource_exhausted", code, err)
	}
}
