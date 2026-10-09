package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// errSign marks a signing failure inside the renewal's writer job.
var errSign = errors.New("gateway: sign the renewed certificate")

// RenewCert signs a certificate for the CSR's key (02g "Certificate renewal
// (A4)"). It checks, in order: a verified client certificate, whose CN is
// the machine id and whose serial is the peer serial (never anything from
// the request); the enroll CSR rule; a key other than the presented
// certificate's. statedb.Renew then needs a live machine row and a known,
// unrevoked peer serial, returns the stored certificate for the same machine
// and key, or signs one inside its writer job with ca.AgentLifetime, never
// past the CA. The old serial is not revoked.
func (s *agentService) RenewCert(ctx context.Context, req *connect.Request[agentv1.RenewCertRequest],
) (*connect.Response[agentv1.RenewCertResponse], error) {
	peer := req.Peer().Addr
	leaf := peerCert(ctx)
	if leaf == nil {
		return nil, s.renewRefused(ctx, "", peer, "no_client_cert", refusal(connect.CodeUnauthenticated,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "RenewCert needs the machine's client certificate"))
	}
	id := leaf.Subject.CommonName
	csr, err := ca.ParseCSR(req.Msg.GetCsrPem())
	if err != nil {
		return nil, s.renewRefused(ctx, id, peer, "csr_invalid", refusal(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_RENEW_CSR_INVALID, "the CSR is not a valid renewal request"))
	}
	if current, ok := leaf.PublicKey.(ed25519.PublicKey); ok && current.Equal(csr.Key) {
		return nil, s.renewRefused(ctx, id, peer, "csr_same_key", refusal(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_RENEW_CSR_INVALID, "the CSR must be for a new key"))
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	r, err := statedb.Renew(ctx, s.store, statedb.Renewal{
		MachineID: id, PeerSerial: ca.SerialHex(leaf.SerialNumber), CSRKeyHash: csr.KeyHash, Now: now,
		Sign: func() (statedb.Signed, error) {
			c, err := s.auth.IssueAgent(id, csr.Key, now, s.random)
			if err != nil {
				return statedb.Signed{}, fmt.Errorf("%w: %w", errSign, err)
			}
			return statedb.Signed{CertDER: c.CertDER, Serial: ca.SerialHex(c.Serial), NotAfter: c.NotAfter}, nil
		},
	})
	if err != nil {
		return nil, s.renewOutcome(ctx, id, peer, err)
	}
	issued, err := x509.ParseCertificate(r.CertDER)
	if err != nil {
		return nil, s.renewFailed(ctx, id, "encode", err)
	}
	s.renewed(ctx, id, r, issued.NotAfter)
	return connect.NewResponse(&agentv1.RenewCertResponse{
		CertPem:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.CertDER}),
		CaChainPem:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.id.CACert}),
		CertNotAfterTime: timestamppb.New(issued.NotAfter),
	}), nil
}

type peerKey struct{}

// withPeerCert hands the verified client certificate of the TLS connection,
// if any, to the handlers through the request context. Only a certificate
// that chained to the hub CA during the handshake is passed on.
func withPeerCert(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
			r = r.WithContext(context.WithValue(r.Context(), peerKey{}, r.TLS.VerifiedChains[0][0]))
		}
		next.ServeHTTP(w, r)
	})
}

// peerCert is the verified client certificate, or nil.
func peerCert(ctx context.Context) *x509.Certificate {
	c, _ := ctx.Value(peerKey{}).(*x509.Certificate)
	return c
}
