package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// MaxMessageBytes caps a request message on the agent listener (02e: the
// 1 MiB message cap).
const MaxMessageBytes = 1 << 20

// maxInstallID caps EnrollRequest.install_id (02g).
const maxInstallID = 64

// agentService is AgentService. Enroll, RenewCert and Connect are built;
// Terminal and SessionLog answer Unimplemented until their rows land.
type agentService struct {
	agentv1connect.UnimplementedAgentServiceHandler
	*enrollDeps
	*connectDeps
}

// Enroll checks, in order: install_id, the token's format and CA hash
// (ENROLL_TOKEN_UNKNOWN), the proto range, the CSR, then the store's
// read-pool pre-check (unknown, replay or used, expired). A token naming
// a previous machine the request does not contradict re-enrolls it (the
// same id back to ENROLLED with a new serial); a deleted previous
// machine enrolls fresh. A fresh token is burned by one writer job that
// inserts the machine, its serial and the replayable response. Nothing
// before the writer job burns the token. A client certificate on the
// connection is ignored.
func (s *agentService) Enroll(ctx context.Context, req *connect.Request[agentv1.EnrollRequest],
) (*connect.Response[agentv1.EnrollResponse], error) {
	m, peer := req.Msg, req.Peer().Addr
	if err := checkInstallID(m.GetInstallId()); err != nil {
		return nil, s.refused(ctx, peer, err)
	}
	tok, err := ca.ParseToken(m.GetToken())
	if err != nil || tok.CAFingerprint != s.caHash {
		return nil, s.refused(ctx, peer, refusal(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_UNKNOWN,
			"this hub did not issue the token"))
	}
	if _, ok := protoversion.Negotiate(m.GetProtoMin(), m.GetProtoMax()); !ok {
		return nil, s.refused(ctx, peer, refusal(connect.CodeFailedPrecondition, typev1.ReasonCode_REASON_CODE_ENROLL_PROTO_UNSUPPORTED,
			"no proto version in common with the hub"))
	}
	csr, err := ca.ParseCSR(m.GetCsrPem())
	if err != nil {
		return nil, s.refused(ctx, peer, refusal(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_ENROLL_CSR_INVALID,
			"the CSR is not a valid enroll request"))
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	key := statedb.EnrollKey{SecretHash: tok.SecretHash, InstallID: m.GetInstallId(), CSRKeyHash: csr.KeyHash}
	check, err := statedb.CheckEnroll(ctx, s.store, key, now)
	if err != nil {
		return nil, s.storeOutcome(ctx, peer, err)
	}
	enrolled := check.Replay
	if enrolled == nil && check.Token.PreviousMachine != "" {
		if prev := m.GetPreviousMachineId().GetValue(); prev != "" && prev != check.Token.PreviousMachine {
			return nil, s.refused(ctx, peer, refusal(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_ENROLL_PREVIOUS_MACHINE_MISMATCH,
				"the request names another machine than the token"))
		}
		e, err := s.enrollPrevious(ctx, peer, key, csr, m, check.Token.PreviousMachine, now)
		if errors.Is(err, statedb.ErrPreviousGone) {
			// The previous machine was deleted after the token was
			// issued: the request enrolls a fresh machine (02g).
			e, err = s.enrollNew(ctx, peer, key, csr, m.GetFacts(), now)
		}
		if err != nil {
			return nil, err
		}
		enrolled = &e
	}
	if enrolled == nil {
		e, err := s.enrollNew(ctx, peer, key, csr, m.GetFacts(), now)
		if err != nil {
			return nil, err
		}
		enrolled = &e
	}
	res, err := s.response(*enrolled)
	if err != nil {
		return nil, s.failed(ctx, "encode", err)
	}
	s.completed(ctx, *enrolled)
	return connect.NewResponse(res), nil
}

// enrollPrevious signs the certificate for the token's previous machine
// and submits the re-enroll writer job, which returns the same machine
// id to ENROLLED with the new serial. A deleted previous machine is
// [statedb.ErrPreviousGone]: the caller enrolls fresh instead.
func (s *agentService) enrollPrevious(ctx context.Context, peer string, key statedb.EnrollKey, csr ca.CSR,
	m *agentv1.EnrollRequest, previous string, now time.Time,
) (statedb.Enrolled, error) {
	cert, err := s.auth.IssueAgent(previous, csr.Key, now, s.random)
	if err != nil {
		return statedb.Enrolled{}, s.failed(ctx, "sign", err)
	}
	facts := m.GetFacts()
	if facts == nil {
		facts = &agentv1.MachineFacts{}
	}
	factsJSON, err := protojson.Marshal(facts)
	if err != nil {
		return statedb.Enrolled{}, s.failed(ctx, "encode", err)
	}
	e, err := statedb.EnrollPrevious(ctx, s.store, statedb.ReenrollMachine{
		Key: key, PreviousMachine: previous, RequestPrevious: m.GetPreviousMachineId().GetValue(),
		CertDER: cert.CertDER, Serial: ca.SerialHex(cert.Serial), CertNotAfter: cert.NotAfter,
		ReportedHostname: facts.GetHostname(), Facts: string(factsJSON), Now: now,
	}, nil)
	if errors.Is(err, statedb.ErrPreviousGone) {
		// Raw, so the caller enrolls fresh instead of refusing.
		return statedb.Enrolled{}, err
	}
	if err != nil {
		return statedb.Enrolled{}, s.storeOutcome(ctx, peer, err)
	}
	return e, nil
}

// enrollNew mints the machine id, signs the certificate and submits the
// writer job. The writer may still answer a replay (another Enroll with
// the same key won the burn) or a token error.
func (s *agentService) enrollNew(ctx context.Context, peer string, key statedb.EnrollKey, csr ca.CSR,
	facts *agentv1.MachineFacts, now time.Time,
) (statedb.Enrolled, error) {
	id, err := ulid.New(now, s.random)
	if err != nil {
		return statedb.Enrolled{}, s.failed(ctx, "random", err)
	}
	cert, err := s.auth.IssueAgent(id, csr.Key, now, s.random)
	if err != nil {
		return statedb.Enrolled{}, s.failed(ctx, "sign", err)
	}
	if facts == nil {
		facts = &agentv1.MachineFacts{}
	}
	factsJSON, err := protojson.Marshal(facts)
	if err != nil {
		return statedb.Enrolled{}, s.failed(ctx, "encode", err)
	}
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the waiter may have left
	e, err := statedb.Enroll(ctx, s.store, statedb.NewMachine{
		Key: key, MachineID: id, CertDER: cert.CertDER, Serial: ca.SerialHex(cert.Serial), CertNotAfter: cert.NotAfter,
		ReportedHostname: facts.GetHostname(), Facts: string(factsJSON), Now: now,
	}, func(enrolled statedb.Enrolled) {
		// The enroll job committed: the machine is new to Watch
		// subscribers (a replay publishes nothing; the first enroll
		// already did). This runs on the writer in commit order (#393).
		s.publishMachineCreated(pctx, enrolled.Row, enrolled.Replayed)
	})
	if err != nil {
		return statedb.Enrolled{}, s.storeOutcome(ctx, peer, err)
	}
	return e, nil
}

// response rebuilds EnrollResponse from hub_identity and the stored
// certificate, the same way for a first answer and a replay.
func (s *agentService) response(e statedb.Enrolled) (*agentv1.EnrollResponse, error) {
	leaf, err := x509.ParseCertificate(e.CertDER)
	if err != nil {
		return nil, err
	}
	return &agentv1.EnrollResponse{
		MachineId:        &typev1.MachineId{Value: e.MachineID},
		CertPem:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.CertDER}),
		CaChainPem:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.id.CACert}),
		CaFingerprint:    hex.EncodeToString(s.caHash[:]),
		CertNotAfterTime: timestamppb.New(leaf.NotAfter),
		AccountSalt:      s.id.AccountSalt,
		SaltVersion:      uint32(s.id.SaltVersion), //nolint:gosec // G115: salt_version is a small positive integer (CHECK >= 1)
		HubAddresses:     s.id.HubAddresses,
		MachineName:      e.MachineName,
		ExpectedHostname: e.ExpectedHostname,
		FleetId:          s.id.FleetID,
		Replayed:         e.Replayed,
	}, nil
}

// checkInstallID applies the 02g rule: 1 to 64 printable ASCII characters
// with no space. Too long is FIELD_TOO_LARGE; any other break is
// INVALID_ARGUMENT.
func checkInstallID(id string) *connect.Error {
	if len(id) > maxInstallID {
		return refusal(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
			"install_id is longer than 64 bytes")
	}
	ok := id != ""
	for i := 0; ok && i < len(id); i++ {
		ok = id[i] > ' ' && id[i] < 0x7f
	}
	if !ok {
		return refusal(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"install_id must be 1 to 64 printable ASCII characters without spaces")
	}
	return nil
}

// storeOutcome maps a statedb answer: the token errors are the caller's
// (refused); a previous machine that is gone enrolls fresh upstream
// (never here); anything else is the store's, UNAVAILABLE and retryable.
// A cancelled write has an unknown outcome; the retry replays.
func (s *agentService) storeOutcome(ctx context.Context, peer string, err error) error {
	switch {
	case errors.Is(err, statedb.ErrTokenUnknown):
		return s.refused(ctx, peer, refusal(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_UNKNOWN,
			"this hub did not issue the token"))
	case errors.Is(err, statedb.ErrTokenExpired):
		return s.refused(ctx, peer, refusal(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_EXPIRED,
			"the token has expired; ask for a new one"))
	case errors.Is(err, statedb.ErrTokenUsed):
		return s.refused(ctx, peer, refusal(connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_USED,
			"the token was already used by another install or key"))
	case errors.Is(err, statedb.ErrPreviousMismatch):
		return s.refused(ctx, peer, refusal(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_ENROLL_PREVIOUS_MACHINE_MISMATCH,
			"the request names another machine than the token"))
	case errors.Is(err, statedb.ErrNotReenrollable):
		return s.refused(ctx, peer, refusal(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_MACHINE_NOT_REENROLLABLE,
			"the previous machine is neither revoked nor past its certificate lifetime"))
	default:
		return s.failed(ctx, "store", err)
	}
}

func caHash(caDER []byte) [32]byte { return sha256.Sum256(caDER) }
