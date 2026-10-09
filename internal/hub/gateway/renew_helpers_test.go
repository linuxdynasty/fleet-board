package gateway_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// machine is an enrolled agent: its machine id, the key it enrolled with
// and the client certificate it presents.
type machine struct {
	id   string
	key  agentKey
	cert tls.Certificate
	leaf *x509.Certificate
}

// enrollMachine enrolls a fresh machine through the listener.
func (h *hub) enrollMachine(t *testing.T) machine {
	t.Helper()
	k := newAgentKey(t)
	res := h.mustEnroll(t, enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), k, "install-1"))
	cert := tlsCertOf(t, res, k)
	return machine{id: res.GetMachineId().GetValue(), key: k, cert: cert, leaf: leafOf(t, res.GetCertPem())}
}

// renew calls RenewCert presenting certs (none for an anonymous caller).
func (h *hub) renew(t *testing.T, csr []byte, certs ...tls.Certificate) (*agentv1.RenewCertResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := h.client(t, certs...).RenewCert(ctx, connect.NewRequest(&agentv1.RenewCertRequest{CsrPem: csr}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (h *hub) mustRenew(t *testing.T, csr []byte, cert tls.Certificate) *agentv1.RenewCertResponse {
	t.Helper()
	res, err := h.renew(t, csr, cert)
	if err != nil {
		t.Fatalf("RenewCert: %v", err)
	}
	return res
}

// tlsCertFor is the client certificate for leaf held with k's key.
func tlsCertFor(leaf *x509.Certificate, k agentKey) tls.Certificate {
	return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: k.priv}
}

// leafOf parses a PEM holding exactly one CERTIFICATE block.
func leafOf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatalf("not one CERTIFICATE block: %q", certPEM)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func serialOf(leaf *x509.Certificate) string { return ca.SerialHex(leaf.SerialNumber) }

// serialRows is the number of rows in cert_serials.
func (h *hub) serialRows(t *testing.T) int {
	t.Helper()
	var n int
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT count(*) FROM cert_serials`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// serialRevoked reports whether the certificate's cert_serials row has a
// revoked_time.
func (h *hub) serialRevoked(t *testing.T, leaf *x509.Certificate) bool {
	t.Helper()
	var revoked sql.NullInt64
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT revoked_time FROM cert_serials WHERE serial = ?`,
			ca.SerialHex(leaf.SerialNumber)).Scan(&revoked)
	})
	if err != nil {
		t.Fatal(err)
	}
	return revoked.Valid
}

// exec runs one statement through the store's writer: test setup for state
// the hub has no RPC for yet (RevokeMachine and DeleteMachine are row 13a).
func (h *hub) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	err := h.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), query, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
