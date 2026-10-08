package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

var errRevoked = errors.New("gateway: the client certificate is revoked")

// verifyConnection is tls.Config.VerifyConnection: it runs after the chain
// check on every handshake, resumed TLS 1.3 sessions included, and refuses
// a client certificate whose serial is revoked. No client certificate is
// fine (Enroll).
func verifyConnection(revoked RevokedSet, tel *telemetry.Telemetry, log *slog.Logger) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return nil
		}
		leaf := cs.PeerCertificates[0]
		serial := ca.SerialHex(leaf.SerialNumber)
		if !revoked.Revoked(serial) {
			return nil
		}
		ctx := context.Background()
		tel.CountAuthFailure(ctx, telemetry.ReasonRevoked)
		log.WarnContext(ctx, "tls.client_cert_revoked", "serial", serial, "machine_id", leaf.Subject.CommonName)
		return errRevoked
	}
}
