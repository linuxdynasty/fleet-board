package gateway_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// A machine revoked through the row-13a path (the revoke's store job,
// then the serials staged onto the hub's own revocation list) has its
// next handshake refused: the serials the job revoked are the ones the
// TLS verify step reads.
func TestRevokeMachineRefusesTheNextHandshake(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	cert, leaf := enrolledCert(t, h, "install-1")
	id := leaf.Subject.CommonName
	ctx := context.Background()
	now := h.clock.now()

	revoked, err := statedb.RevokeMachine(ctx, h.store, statedb.MachineRevoke{
		MachineWrite: statedb.MachineWrite{
			Principal: "local:alice", RequestID: "req-1",
			RequestHash: sha256.Sum256([]byte("req-1")), Now: now,
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
	var add []revocation.Entry
	for _, s := range revoked.Serials {
		add = append(add, revocation.Entry{Serial: s.Serial, NotAfter: s.NotAfter})
	}
	if err := h.revoked.Stage(add); err != nil {
		t.Fatal(err)
	}

	fresh := clientTLS(h.fixture, "localhost")
	fresh.Certificates = []tls.Certificate{cert}
	if _, err := handshake(t, h.addr, fresh); err == nil || !strings.Contains(err.Error(), "remote error") {
		t.Errorf("handshake after the revoke: %v, want the server to refuse it", err)
	}
}
