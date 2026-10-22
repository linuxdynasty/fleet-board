package gateway_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// previousToken stores an enroll token naming previous and returns its
// text.
func previousToken(t *testing.T, h *hub, previous string, notAfter time.Time) string {
	t.Helper()
	tok, err := ca.NewToken(h.id.CACert, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reqID := fmt.Sprintf("re-enroll-%d", requestSeq.Add(1))
	err = statedb.InsertEnrollToken(context.Background(), h.store, statedb.EnrollToken{
		SecretHash: tok.SecretHash, MachineName: "laptop", Labels: []string{"home"},
		NotAfter: notAfter.Truncate(time.Millisecond), PreviousMachine: previous,
		Created: h.clock.now(), Principal: "local:alice", RequestID: reqID,
		RequestHash: sha256.Sum256([]byte(reqID)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Reveal()
}

// revokeMachine marks id REVOKED straight in the store (the owner API
// lives on the admin socket, not the agent listener under test).
func revokeMachine(t *testing.T, h *hub, id string) {
	t.Helper()
	_, err := statedb.RevokeMachine(context.Background(), h.store, statedb.MachineRevoke{
		MachineWrite: statedb.MachineWrite{
			Principal: "local:alice", RequestID: "rev-" + id,
			RequestHash: sha256.Sum256([]byte("rev-" + id)), Now: h.clock.now(),
		},
		MachineID: id,
		CloseOpen: func(ctx context.Context, tx *sql.Tx, machineID string, now time.Time) ([]statedb.RevokedCommand, error) {
			closed, err := outbox.CloseForRevoke(ctx, tx, machineID, now)
			if err != nil {
				return nil, err
			}
			var out []statedb.RevokedCommand
			for _, c := range closed {
				out = append(out, statedb.RevokedCommand{CommandID: c.CommandID})
			}
			return out, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func certSerial(t *testing.T, certPEM []byte) string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return ca.SerialHex(leaf.SerialNumber)
}

func TestReenrollKeepsTheMachineID(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	first := h.mustEnroll(t, enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1"))
	id := first.GetMachineId().GetValue()
	if id == "" {
		t.Fatal("first enroll has no machine id")
	}
	revokeMachine(t, h, id)

	tok := previousToken(t, h, id, h.clock.now().Add(time.Hour))
	mismatchTok := previousToken(t, h, id, h.clock.now().Add(time.Hour))
	again, err := h.enroll(t, enrollRequest(tok, newAgentKey(t), "install-2"))
	if err != nil {
		t.Fatalf("re-enroll: %v", err)
	}
	if again.GetMachineId().GetValue() != id {
		t.Errorf("re-enroll id = %q, want %q", again.GetMachineId().GetValue(), id)
	}
	if certSerial(t, again.GetCertPem()) == certSerial(t, first.GetCertPem()) {
		t.Error("re-enroll returned the same certificate")
	}
	if n := h.machineCount(t); n != 1 {
		t.Errorf("machines = %d, want 1", n)
	}

	hint := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-3")
	_, err = h.enroll(t, hint)
	if err != nil {
		t.Fatalf("fresh enroll after re-enroll: %v", err)
	}

	mismatch := enrollRequest(mismatchTok, newAgentKey(t), "install-4")
	mismatch.PreviousMachineId = &typev1.MachineId{Value: "01jother0000000000000001"}
	_, err = h.enroll(t, mismatch)
	r := refusalOf(t, err)
	if r.code != connect.CodeFailedPrecondition || r.reason != typev1.ReasonCode_REASON_CODE_ENROLL_PREVIOUS_MACHINE_MISMATCH {
		t.Errorf("mismatch = %v %v, want FAILED_PRECONDITION ENROLL_PREVIOUS_MACHINE_MISMATCH", r.code, r.reason)
	}
}

func TestReenrollAfterDeleteEnrollsFresh(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	first := h.mustEnroll(t, enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1"))
	id := first.GetMachineId().GetValue()
	revokeMachine(t, h, id)
	tok := previousToken(t, h, id, h.clock.now().Add(time.Hour))
	if _, _, err := statedb.DeleteMachine(context.Background(), h.store, statedb.MachineDelete{
		MachineWrite: statedb.MachineWrite{
			Principal: "local:alice", RequestID: "del-" + id,
			RequestHash: sha256.Sum256([]byte("del-" + id)), Now: h.clock.now(),
		},
		MachineID: id,
	}, nil); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.enroll(t, enrollRequest(tok, newAgentKey(t), "install-2"))
	if err != nil {
		t.Fatalf("enroll after delete: %v", err)
	}
	if fresh.GetMachineId().GetValue() == id {
		t.Errorf("fresh id = %q, want a new id", id)
	}
	if n := h.machineCount(t); n != 1 {
		t.Errorf("machines = %d, want 1", n)
	}
}
